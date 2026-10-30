// Resource/desktop integration fixture using the real StartedService. It has no
// installed-service identity, OS proxy API or privileged daemon relay.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"

	"github.com/sagernet/sing-box/daemon"
	"github.com/sagernet/sing-box/experimental/libbox"
	"github.com/sagernet/sing-box/include"
	"google.golang.org/grpc"
)

func validate(raw []byte) map[string]any {
	var config map[string]any
	must(json.Unmarshal(raw, &config))
	inbounds, ok := config["inbounds"].([]any)
	if !ok || len(inbounds) == 0 {
		panic("synthetic loopback inbounds required")
	}
	for _, entry := range inbounds {
		input := entry.(map[string]any)
		if (input["type"] != "mixed" && input["type"] != "http") || input["listen"] != "127.0.0.1" || input["set_system_proxy"] == true {
			panic("loopback only; no system proxy")
		}
	}
	for _, entry := range config["outbounds"].([]any) {
		o := entry.(map[string]any)
		if o["type"] != "selector" && o["server"] != "127.0.0.1" {
			panic("synthetic loopback proxy outbounds only")
		}
	}
	return inbounds[0].(map[string]any)
}

func main() {
	file := flag.String("config", "", "synthetic core JSON")
	directory := flag.String("directory", "", "isolated work directory")
	flag.Parse()
	raw, err := os.ReadFile(*file)
	must(err)
	input := validate(raw)
	must(os.MkdirAll(*directory, 0700))
	must(libbox.Setup(&libbox.SetupOptions{BasePath: *directory, WorkingPath: *directory, TempPath: *directory}))
	ctx := include.Context(context.Background())
	service := daemon.NewStartedService(daemon.ServiceOptions{Context: ctx, LogMaxLines: 128})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	server := grpc.NewServer(daemon.MessageSizeServerOptions()...)
	daemon.RegisterStartedServiceServer(server, service)
	go server.Serve(listener)
	control, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	var controlMutex sync.Mutex
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controlMutex.Lock()
		defer controlMutex.Unlock()
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var err error
		switch r.URL.Path {
		case "/reload":
			var next []byte
			next, err = io.ReadAll(io.LimitReader(r.Body, 2<<20))
			if err == nil {
				validate(next)
				raw = next
				err = service.StartOrReloadService(ctx, string(raw), nil)
			}
		case "/start":
			err = service.StartOrReloadService(ctx, string(raw), nil)
		case "/stop":
			err = service.CloseService()
		default:
			w.WriteHeader(404)
			return
		}
		if err != nil {
			w.WriteHeader(500)
			io.WriteString(w, "synthetic lifecycle failed: "+err.Error())
			return
		}
		w.WriteHeader(204)
	})
	go http.Serve(control, handler)
	data, _ := json.Marshal(map[string]any{"grpc": listener.Addr().String(), "control": control.Addr().String(), "proxyPort": input["listen_port"]})
	fmt.Println(string(data))
	select {}
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
