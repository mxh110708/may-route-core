package option

import (
	"bytes"
	stdjson "encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/sagernet/sing-box/common/xhttpwire"
)

type V2RayXHTTPOptions struct {
	Path             string                     `json:"path,omitempty"`
	Host             string                     `json:"host,omitempty"`
	Mode             string                     `json:"mode,omitempty" enum:"auto,stream-one,stream-up,packet-up"`
	HTTPVersion      string                     `json:"http_version,omitempty" enum:"auto,1.1,2,3"`
	Extra            stdjson.RawMessage         `json:"extra,omitempty"`
	DownloadSettings *V2RayXHTTPDownloadOptions `json:"download_settings,omitempty"`
}

type V2RayXHTTPDownloadOptions struct {
	ServerOptions
	OutboundTLSOptionsContainer
	Transport *V2RayTransportOptions `json:"transport"`
}

func (o V2RayXHTTPOptions) Validate() error {
	if o.Mode != "" && o.Mode != "auto" && o.Mode != "stream-one" && o.Mode != "stream-up" && o.Mode != "packet-up" {
		return fmt.Errorf("unknown XHTTP mode")
	}
	if o.HTTPVersion != "" && o.HTTPVersion != "auto" && o.HTTPVersion != "1.1" && o.HTTPVersion != "2" && o.HTTPVersion != "3" {
		return fmt.Errorf("unknown XHTTP HTTP version")
	}
	if len(o.Path) > 8192 || strings.ContainsAny(o.Path, "\r\n\x00") {
		return fmt.Errorf("invalid XHTTP path")
	}
	if _, err := url.ParseRequestURI("/" + strings.TrimPrefix(o.Path, "/")); err != nil {
		return fmt.Errorf("invalid XHTTP path: %w", err)
	}
	if strings.ContainsAny(o.Host, "\r\n\x00 /?#@\\") || len(o.Host) > 253 {
		return fmt.Errorf("invalid XHTTP Host")
	}
	if _, err := xhttpwire.ParseExtra(o.Extra); err != nil {
		return err
	}
	if o.DownloadSettings != nil {
		d := o.DownloadSettings
		if o.Mode == "stream-one" {
			return fmt.Errorf("XHTTP stream-one cannot split download")
		}
		if d.Server == "" || d.ServerPort == 0 || d.TLS == nil || !d.TLS.Enabled || d.TLS.Insecure || d.TLS.DisableSNI {
			return fmt.Errorf("XHTTP download requires server and authenticated TLS/Reality")
		}
		if d.Transport == nil || d.Transport.Type != "xhttp" || d.Transport.XHTTPOptions.DownloadSettings != nil {
			return fmt.Errorf("XHTTP download transport must be xhttp without recursive download")
		}
		if err := d.Transport.XHTTPOptions.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (o *V2RayXHTTPOptions) UnmarshalJSON(data []byte) error {
	type plain V2RayXHTTPOptions
	decoder := stdjson.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var value plain
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*o = V2RayXHTTPOptions(value)
	return o.Validate()
}

func (o *V2RayXHTTPOptions) unmarshalTransport(data []byte) error {
	var fields map[string]stdjson.RawMessage
	if err := stdjson.Unmarshal(data, &fields); err != nil {
		return err
	}
	delete(fields, "type")
	data, err := stdjson.Marshal(fields)
	if err != nil {
		return err
	}
	return o.UnmarshalJSON(data)
}
