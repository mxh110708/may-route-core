package option

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sagernet/sing/common/json"
)

func TestXHTTPOptions(t *testing.T) {
	valid := `{"type":"xhttp","path":"/may-stream/","host":"front.example.test","mode":"stream-one"}`
	var options V2RayTransportOptions
	if err := json.Unmarshal([]byte(valid), &options); err != nil {
		t.Fatal(err)
	}
	if options.Type != "xhttp" || options.XHTTPOptions.Host != "front.example.test" {
		t.Fatal("transport identity lost")
	}
	data, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip V2RayTransportOptions
	if err = json.Unmarshal(data, &roundtrip); err != nil || !reflect.DeepEqual(roundtrip.XHTTPOptions, options.XHTTPOptions) {
		t.Fatal("roundtrip changed XHTTP", err)
	}
	for _, mutation := range []struct{ from, to string }{
		{`"stream-one"`, `"unknown"`},
		{`front.example.test`, `bad\r\nHost`},
		{`"mode":"stream-one"`, `"mode":"stream-one","extra":{"unknown":true}`},
		{`"mode":"stream-one"`, `"mode":"stream-one","headers":{"Host":"other.test"}`},
	} {
		t.Run(mutation.to, func(t *testing.T) {
			var invalid V2RayTransportOptions
			if err := json.Unmarshal([]byte(strings.Replace(valid, mutation.from, mutation.to, 1)), &invalid); err == nil {
				t.Fatal("unsupported option accepted")
			}
		})
	}
}

func TestXHTTPExpandedOptions(t *testing.T) {
	for _, mode := range []string{"auto", "stream-one", "stream-up", "packet-up"} {
		for _, version := range []string{"auto", "1.1", "2", "3"} {
			input := `{"type":"xhttp","path":"/encoded%2Fpath/?token=a%2Fb","host":"front.example.test:443","mode":"` + mode + `","http_version":"` + version + `","extra":{"scMaxEachPostBytes":"512-1024","xPaddingBytes":100,"sessionPlacement":"cookie","sessionKey":"sid","xmux":{"maxConnections":2,"hMaxRequestTimes":"2-4"}}}`
			var options, restored V2RayTransportOptions
			if err := json.Unmarshal([]byte(input), &options); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(options)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(encoded, &restored); err != nil || !reflect.DeepEqual(restored, options) {
				t.Fatal("lost extended fields", err)
			}
		}
	}
}
