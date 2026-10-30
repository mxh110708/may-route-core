package xhttpwire

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"golang.org/x/net/http/httpguts"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func newSessionUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

func NewSession() string { return newSessionUUID() }

// Fixed Xray 26.3.27 field inventory, including losslessly retained server fields.
var ExtraFields = map[string]string{
	"headers": "Headers", "xPaddingBytes": "XPaddingBytes", "noGRPCHeader": "NoGRPCHeader", "noSSEHeader": "NoSSEHeader",
	"scMaxEachPostBytes": "ScMaxEachPostBytes", "scMinPostsIntervalMs": "ScMinPostsIntervalMs", "scMaxBufferedPosts": "ScMaxBufferedPosts", "scStreamUpServerSecs": "ScStreamUpServerSecs",
	"xmux": "ReuseConfig", "xPaddingObfsMode": "XPaddingObfsMode", "xPaddingKey": "XPaddingKey", "xPaddingHeader": "XPaddingHeader", "xPaddingPlacement": "XPaddingPlacement", "xPaddingMethod": "XPaddingMethod",
	"uplinkHTTPMethod": "UplinkHTTPMethod", "sessionPlacement": "SessionPlacement", "sessionKey": "SessionKey", "seqPlacement": "SeqPlacement", "seqKey": "SeqKey", "uplinkDataPlacement": "UplinkDataPlacement", "uplinkDataKey": "UplinkDataKey", "uplinkChunkSize": "UplinkChunkSize", "serverMaxHeaderBytes": "ServerMaxHeaderBytes",
}

func ParseExtra(data json.RawMessage) (*Config, error) {
	c := &Config{SessionTable: "uuid"}
	if len(data) == 0 || string(data) == "null" {
		return c, nil
	}
	var input map[string]json.RawMessage
	if err := json.Unmarshal(data, &input); err != nil {
		return nil, err
	}
	converted := make(map[string]json.RawMessage)
	for key, value := range input {
		field, ok := ExtraFields[key]
		if !ok {
			return nil, fmt.Errorf("unknown XHTTP extra field %s", key)
		}
		if field != "ServerMaxHeaderBytes" && (strings.HasSuffix(field, "Bytes") || strings.HasSuffix(field, "Ms") || field == "ScStreamUpServerSecs" || field == "ScMaxBufferedPosts" || field == "UplinkChunkSize") {
			r, err := rangeText(value)
			if err != nil {
				return nil, fmt.Errorf("invalid XHTTP range %s: %w", key, err)
			}
			value, _ = json.Marshal(r)
		}
		if key == "xmux" {
			var entries map[string]json.RawMessage
			if err := json.Unmarshal(value, &entries); err != nil {
				return nil, err
			}
			m := map[string]json.RawMessage{}
			for name, raw := range entries {
				var target string
				switch name {
				case "maxConcurrency":
					target = "MaxConcurrency"
				case "maxConnections":
					target = "MaxConnections"
				case "cMaxReuseTimes":
					target = "CMaxReuseTimes"
				case "hMaxRequestTimes":
					target = "HMaxRequestTimes"
				case "hMaxReusableSecs":
					target = "HMaxReusableSecs"
				case "hKeepAlivePeriod":
					target = "HKeepAlivePeriod"
				default:
					return nil, fmt.Errorf("unknown XHTTP xmux field %s", name)
				}
				if name != "hKeepAlivePeriod" {
					s, err := rangeText(raw)
					if err != nil {
						return nil, err
					}
					raw, _ = json.Marshal(s)
				}
				m[target] = raw
			}
			value, _ = json.Marshal(m)
		}
		converted[field] = value
	}
	encoded, _ := json.Marshal(converted)
	if err := json.Unmarshal(encoded, c); err != nil {
		return nil, err
	}
	if c.XPaddingKey == "" {
		c.XPaddingKey = "x_padding"
	}
	if c.XPaddingHeader == "" {
		c.XPaddingHeader = "X-Padding"
	}
	if c.XPaddingPlacement == "" {
		c.XPaddingPlacement = PlacementQueryInHeader
	}
	if c.XPaddingMethod == "" {
		c.XPaddingMethod = "repeat-x"
	}
	c.UplinkHTTPMethod = strings.ToUpper(c.GetNormalizedUplinkHTTPMethod())
	if c.UplinkDataKey == "" {
		switch c.GetNormalizedUplinkDataPlacement() {
		case PlacementCookie:
			c.UplinkDataKey = "x_data"
		case PlacementHeader, PlacementAuto:
			c.UplinkDataKey = "X-Data"
		}
	}
	return c, c.Validate()
}

func (c *Config) ValidateMode() error {
	if c.GetNormalizedUplinkHTTPMethod() == "GET" && c.NormalizedMode() != "packet-up" {
		return fmt.Errorf("XHTTP GET upload requires explicit packet-up")
	}
	if (c.UplinkDataPlacement == PlacementHeader || c.UplinkDataPlacement == PlacementCookie) && c.NormalizedMode() != "packet-up" {
		return fmt.Errorf("XHTTP header/cookie payload requires explicit packet-up")
	}
	return nil
}

func rangeText(data json.RawMessage) (string, error) {
	var text string
	if json.Unmarshal(data, &text) == nil {
		if _, err := ParseRange(text, "0"); err != nil {
			return "", err
		}
		return text, nil
	}
	var n int
	if json.Unmarshal(data, &n) == nil && n >= 0 {
		return strconv.Itoa(n), nil
	}
	var value map[string]int
	if err := json.Unmarshal(data, &value); err == nil && len(value) == 2 {
		from, f := value["from"]
		to, t := value["to"]
		if f && t && from >= 0 && to >= from {
			return fmt.Sprintf("%d-%d", from, to), nil
		}
	}
	return "", fmt.Errorf("expected nonnegative integer, range string or from/to object")
}

func (c *Config) URL() url.URL {
	parts := strings.SplitN(c.Path, "?", 2)
	u := url.URL{Scheme: "https", Host: c.Host, Path: c.NormalizedPath()}
	if len(parts) > 1 {
		u.RawQuery = parts[1]
	}
	return u
}

func (c *Config) Validate() error {
	if c.XPaddingMethod != "" && c.XPaddingMethod != "repeat-x" && c.XPaddingMethod != "tokenish" {
		return fmt.Errorf("invalid XHTTP padding method")
	}
	if strings.ContainsAny(c.GetNormalizedUplinkHTTPMethod(), "()<>@,;:\\\"/[]?={} \t\r\n\x00") {
		return fmt.Errorf("invalid XHTTP upload HTTP method")
	}
	valid := func(value string, allowed ...string) bool {
		if value == "" {
			return true
		}
		for _, v := range allowed {
			if value == v {
				return true
			}
		}
		return false
	}
	if !valid(c.XPaddingPlacement, PlacementHeader, PlacementCookie, PlacementQuery, PlacementQueryInHeader) || !valid(c.SessionPlacement, PlacementPath, PlacementHeader, PlacementCookie, PlacementQuery) || !valid(c.SeqPlacement, PlacementPath, PlacementHeader, PlacementCookie, PlacementQuery) || !valid(c.UplinkDataPlacement, PlacementBody, PlacementHeader, PlacementCookie, PlacementAuto) {
		return fmt.Errorf("invalid XHTTP metadata placement")
	}
	seen := make(map[string]bool)
	for k, v := range c.Headers {
		canonical := http.CanonicalHeaderKey(k)
		if !httpguts.ValidHeaderFieldName(k) || !httpguts.ValidHeaderFieldValue(v) || seen[canonical] {
			return fmt.Errorf("invalid XHTTP header")
		}
		seen[canonical] = true
		switch http.CanonicalHeaderKey(k) {
		case "Host", "Content-Length", "Transfer-Encoding", "Connection":
			return fmt.Errorf("XHTTP reserved header %s", k)
		}
	}
	if c.ServerMaxHeaderBytes < 0 {
		return fmt.Errorf("invalid XHTTP server header limit")
	}
	for _, v := range []string{c.SessionKey, c.SeqKey, c.XPaddingKey, c.XPaddingHeader, c.UplinkDataKey} {
		if strings.ContainsAny(v, "\r\n\x00;= ") {
			return fmt.Errorf("invalid XHTTP metadata key")
		}
	}
	for _, get := range []func() (Range, error){c.GetNormalizedXPaddingBytes, c.GetNormalizedScMaxEachPostBytes, c.GetNormalizedScMinPostsIntervalMs, c.GetNormalizedUplinkChunkSize} {
		r, err := get()
		if err != nil {
			return err
		}
		if r.Max > 16<<20 {
			return fmt.Errorf("XHTTP configured range exceeds 16 MiB resource bound")
		}
	}
	post, err := c.GetNormalizedScMaxEachPostBytes()
	if err != nil {
		return err
	}
	if post.Min <= 0 {
		return fmt.Errorf("XHTTP upload batch must be positive")
	}
	if c.ReuseConfig != nil {
		a, b, err := c.ReuseConfig.ResolveManagerConfig()
		if err != nil {
			return err
		}
		if a.Max > 0 && b.Max > 0 {
			return fmt.Errorf("XHTTP xmux maxConcurrency and maxConnections are mutually exclusive")
		}
		if _, _, _, err = c.ReuseConfig.ResolveEntryConfig(); err != nil {
			return err
		}
	}
	return nil
}
