package xhttpwire

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestWireMetadataAndPadding(t *testing.T) {
	for _, placement := range []string{"path", "query", "cookie", "header"} {
		c, err := ParseExtra(json.RawMessage(`{"sessionPlacement":"` + placement + `","seqPlacement":"` + placement + `","sessionKey":"May-Session","seqKey":"May-Seq","xPaddingBytes":"100-200"}`))
		if err != nil {
			t.Fatal(err)
		}
		c.Host = "front.example.test"
		c.Path = "/literal%2Fpart/?keep=a%2Fb"
		u := c.URL()
		req, _ := http.NewRequest("POST", u.String(), nil)
		if err = c.FillPacketRequest(req, "session", "17", []byte("body")); err != nil {
			t.Fatal(err)
		}
		session, seq := c.ExtractMetaFromRequest(req, c.NormalizedPath())
		if session != "session" || seq != "17" {
			t.Fatal("lost metadata", placement)
		}
		if req.URL.Query().Get("keep") != "a/b" || !strings.Contains(req.URL.EscapedPath(), "%252F") {
			t.Fatal("query/path changed")
		}
	}
}

func TestExtraValidation(t *testing.T) {
	for _, data := range []string{
		`{"unknown":1}`, `{"xPaddingBytes":{}}`, `{"xPaddingBytes":{"foo":1,"bar":2}}`, `{"scMaxEachPostBytes":"0-1"}`, `{"uplinkHTTPMethod":"POST\r\nInjected"}`,
		`{"headers":{"Content-Length":"4"}}`, `{"xPaddingPlacement":"unknown"}`, `{"xmux":{"unknown":1}}`, `{"xmux":{"maxConnections":2,"maxConcurrency":3}}`,
	} {
		if _, err := ParseExtra(json.RawMessage(data)); err == nil {
			t.Fatalf("accepted invalid extra %s", data)
		}
	}
	c, err := ParseExtra(json.RawMessage(`{"serverMaxHeaderBytes":32768,"noSSEHeader":true,"scStreamUpServerSecs":"5-10","scMaxBufferedPosts":12,"xmux":{"hKeepAlivePeriod":-1}}`))
	if err != nil || c.ServerMaxHeaderBytes != 32768 || !c.NoSSEHeader {
		t.Fatal("server/client fields lost", err)
	}
}
