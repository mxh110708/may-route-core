"""Production Route preparation/decision + real StartedService on loopback.

No installed daemon, SCM, OS proxy, TUN, user profile or public endpoint.
"""
import argparse
import concurrent.futures
import contextlib
import copy
import hashlib
import http.client
import http.server
import importlib.util
import json
import os
from pathlib import Path
import queue
import ssl
import subprocess
import tempfile
import threading
import time
import urllib.parse
import uuid

spec=importlib.util.spec_from_file_location('fixture',Path(__file__).with_name('xhttp-runtime.py'))
f=importlib.util.module_from_spec(spec);spec.loader.exec_module(f)


def run(args):
    assert hashlib.sha256(args.xray.read_bytes()).hexdigest()==f.XRAY_SHA256
    result={'scope':'real StartedService, production Route runtime preparation, probe routes and failover decision; loopback only; installed privileged worker and OS proxy excluded','steps':[],'complete':False}
    with tempfile.TemporaryDirectory(prefix='lifecycle-',dir=args.output.parent) as temporary:
        root=Path(temporary);runtime=f.Runtime(root);ca,cert,key=f.pki(root)
        slow_started=threading.Event()
        class Origin(f.Origin):
            def do_GET(self):
                if self.path!='/slow':return super().do_GET()
                self.send_response(200);self.send_header('Content-Length',str(4*1024*1024));self.end_headers();slow_started.set()
                try:
                    for _ in range(1024):self.wfile.write(b'x'*4096);self.wfile.flush();time.sleep(.01)
                except OSError:pass
        origin=http.server.ThreadingHTTPServer(('127.0.0.1',0),Origin);origin.daemon_threads=True
        ctx=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);ctx.load_cert_chain(str(root/'cert.pem'),str(root/'key.pem'));origin.socket=ctx.wrap_socket(origin.socket,server_side=True)
        health=http.server.ThreadingHTTPServer(('127.0.0.1',0),f.Origin);health.daemon_threads=True
        for server in (origin,health):threading.Thread(target=server.serve_forever,daemon=True).start()
        backend=None;log=None
        try:
            server_port,proxy_port,api_port=f.port(),f.port(),f.port();secret=uuid.uuid4().hex;user=str(uuid.uuid4())
            extra={'sessionPlacement':'header','sessionKey':'X-Session','seqPlacement':'query','seqKey':'sequence','xPaddingObfsMode':True,'xPaddingPlacement':'cookie','xPaddingKey':'pad','xPaddingMethod':'tokenish','xmux':{'maxConcurrency':2,'hMaxRequestTimes':3,'hKeepAlivePeriod':-1}}
            server={'log':{'loglevel':'warning'},'inbounds':[{'listen':'127.0.0.1','port':server_port,'protocol':'vless','settings':{'decryption':'none','clients':[{'id':user}]},'streamSettings':{'network':'xhttp','security':'tls','tlsSettings':{'alpn':['h2'],'certificates':[{'certificate':cert.splitlines(),'key':key.splitlines()}]},'xhttpSettings':{'path':'/literal%2Fpath/?token=a%2Fb','host':'front.example.test','mode':'auto','extra':{k:v for k,v in extra.items() if k!='xmux'}}}}],'outbounds':[{'protocol':'freedom'}]}
            good={'type':'vless','tag':'good','server':'127.0.0.1','server_port':server_port,'uuid':user,'packet_encoding':'xudp','tls':{'enabled':True,'server_name':'xhttp.example.test','alpn':['h2'],'certificate':[ca]},'transport':{'type':'xhttp','path':'/literal%2Fpath/?token=a%2Fb','host':'front.example.test','mode':'packet-up','http_version':'2','extra':extra}}
            bad=copy.deepcopy(good);bad.update(tag='bad',uuid=str(uuid.uuid4()))
            config={'log':{'disabled':True},'inbounds':[{'type':'mixed','listen':'127.0.0.1','listen_port':proxy_port,'set_system_proxy':False}],'outbounds':[{'type':'selector','tag':'US-West-LA Entry','outbounds':['bad','good'],'default':'bad','interrupt_exist_connections':True},bad,good],'route':{'final':'US-West-LA Entry'},'experimental':{'clash_api':{'external_controller':f'127.0.0.1:{api_port}','secret':secret}}}
            source=root/'source.private.json';source.write_text(json.dumps(config));prepared=root/'prepared.private.json';candidates=root/'candidates.private.json'
            manifest=root/'manifest.private.json';manifest.write_text(json.dumps({'SyntheticOnly':True,'Source':str(source),'Config':str(prepared),'Candidates':str(candidates),'ProbePorts':[f.port(),f.port()],'HealthUrl':f'http://127.0.0.1:{health.server_port}/','Api':f'http://127.0.0.1:{api_port}','Secret':secret}))
            script=args.route_source/'tests/xhttp-runtime.ts';tsx=args.route_source/'node_modules/tsx/dist/cli.mjs'
            def route(action):
                r=subprocess.run([str(args.node),str(tsx),str(script),action,str(manifest)],cwd=args.route_source,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True,timeout=35,creationflags=subprocess.CREATE_NO_WINDOW)
                if r.returncode:raise RuntimeError('Route fixture failed: '+r.stdout[-2000:])
            route('prepare')
            log=(root/'daemon.log').open('wb');backend=subprocess.Popen([str(args.daemon),'-config',str(prepared),'-directory',str(root/'core-data')],stdout=subprocess.PIPE,stderr=log,creationflags=subprocess.CREATE_NO_WINDOW)
            first=queue.Queue()
            def drain():
                first.put(backend.stdout.readline())
                for _ in backend.stdout:pass
            threading.Thread(target=drain,daemon=True).start();info=json.loads(first.get(timeout=30))
            def control(path,payload=None):
                conn=http.client.HTTPConnection(info['control'],timeout=20)
                try:
                    conn.request('POST',path,json.dumps(payload).encode() if payload is not None else None);response=conn.getresponse();data=response.read()
                    if response.status!=204:raise RuntimeError('StartedService rejected synthetic lifecycle: '+data.decode()[-1000:])
                finally:conn.close()
            def api(method,path,payload=None):
                conn=http.client.HTTPConnection('127.0.0.1',api_port,timeout=10)
                try:
                    conn.request(method,path,json.dumps(payload) if payload else None,headers={'Authorization':'Bearer '+secret,'Content-Type':'application/json'});response=conn.getresponse();data=response.read();assert response.status in (200,204),(response.status,data)
                    return json.loads(data) if data else None
                finally:conn.close()
            with runtime.start(args.xray,server,server_port,True):
                control('/start');before=f.Origin.count
                try:f.https(proxy_port,origin.server_port,ca)
                except (OSError,http.client.HTTPException):pass
                else:raise AssertionError('Invalid initial identity reached origin')
                assert f.Origin.count==before
                route('failover');assert f.https(proxy_port,origin.server_port,ca)==b'198.51.100.22'
                result['steps'].append('production Route confirmed failed identity and selected proven XHTTP backup')
                delay=api('GET','/proxies/good/delay?'+urllib.parse.urlencode({'url':f'http://127.0.0.1:{health.server_port}/','timeout':5000}));assert isinstance(delay.get('delay'),int)
                result['steps'].append('actual on-demand core URL test')
                live=json.loads(prepared.read_text());live['outbounds'][0]['default']='good'
                for mode in ('stream-one','stream-up','packet-up','auto'):
                    for node in live['outbounds'][1:]:node['transport']['mode']=mode
                    control('/reload',live);assert f.https(proxy_port,origin.server_port,ca)==b'198.51.100.22';f.https_upload(proxy_port,origin.server_port,ca,b'route-reload'*32768)
                    result['steps'].append('real StartedService reload retained metadata/query/identity: '+mode)
                with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
                    transfer=pool.submit(f.https,proxy_port,origin.server_port,ca,'/slow');assert slow_started.wait(3)
                    started=time.monotonic();control('/reload',live)
                    try:transfer.result(timeout=5)
                    except (OSError,http.client.HTTPException):pass
                    else:raise AssertionError('Reload kept old active transfer alive')
                    assert time.monotonic()-started<5
                assert f.https(proxy_port,origin.server_port,ca)==b'198.51.100.22'
                result['steps'].append('active transfer closed on reload and new authenticated session reconnected')
                control('/stop')
                try:f.https(proxy_port,origin.server_port,ca)
                except OSError:pass
                else:raise AssertionError('Stopped service still accepted traffic')
                control('/start');assert f.https(proxy_port,origin.server_port,ca)==b'198.51.100.22';control('/stop')
                result['steps'].append('real service stop/start with unchanged full configuration')
            result['complete']=True
        finally:
            if backend is not None and backend.poll() is None:backend.terminate();backend.wait(8)
            if log:log.close()
            for server in (origin,health):server.shutdown();server.server_close()
            result['owned_children_stopped']=(backend is None or backend.poll() is not None) and all(p.poll() is not None for p in runtime.children)
            args.output.write_text(json.dumps(result,indent=2))
    print('PASS: Route preparation/probes/failover, real StartedService loading/reload/cancel/stop/start; all owned processes closed')


if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--daemon',type=Path,required=True);p.add_argument('--xray',type=Path,required=True);p.add_argument('--route-source',type=Path,required=True);p.add_argument('--node',type=Path,required=True);p.add_argument('--output',type=Path,required=True);run(p.parse_args())
