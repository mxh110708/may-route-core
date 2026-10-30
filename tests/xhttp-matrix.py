"""Round 20 fixed-Xray acceptance. Synthetic loopback identities only.

Extends xhttp-runtime.py; never loads private profiles or toggles proxy/TUN.
Use --continue-on-error only to diagnose the matrix: failures remain failures.
"""
import argparse
import base64
import concurrent.futures
import contextlib
import hashlib
import http.server
import importlib.util
import json
import os
from pathlib import Path
import socket
import ssl
import tempfile
import threading
import time
import uuid
import contextlib
import socketserver
import select

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import x25519

spec = importlib.util.spec_from_file_location('fixture', Path(__file__).with_name('xhttp-runtime.py'))
f = importlib.util.module_from_spec(spec)
spec.loader.exec_module(f)


@contextlib.contextmanager
def relay(backend):
    class Handler(socketserver.BaseRequestHandler):
        def handle(self):
            with socket.create_connection(('127.0.0.1',backend),3) as upstream:
                peers=[self.request,upstream]
                while self.server.running:
                    ready,_,_=select.select(peers,[],[],.2)
                    for source in ready:
                        try:
                            data=source.recv(65536)
                            if not data:return
                            self.server.forwarded+=len(data);(upstream if source is self.request else self.request).sendall(data)
                        except OSError:return
    server=socketserver.ThreadingTCPServer(('127.0.0.1',0),Handler);server.daemon_threads=True;server.block_on_close=False;server.running=True;server.forwarded=0
    thread=threading.Thread(target=server.serve_forever);thread.start()
    try:yield server
    finally:server.running=False;server.shutdown();server.server_close();thread.join(3)


def run(args):
    assert os.name == 'nt'
    assert hashlib.sha256(args.xray.read_bytes()).hexdigest() == f.XRAY_SHA256
    result = {'scope': 'isolated loopback TLS/Reality/H1/H2/H3; no installed application or production changes', 'cases': [], 'failures': []}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='matrix-', dir=args.output.parent) as temporary:
        directory = Path(temporary)
        runtime = f.Runtime(directory)
        ca, cert, key = f.pki(directory)
        origin = http.server.ThreadingHTTPServer(('127.0.0.1', 0), f.Origin)
        origin.daemon_threads = True
        tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        tls.load_cert_chain(str(directory/'cert.pem'), str(directory/'key.pem'))
        origin.socket = tls.wrap_socket(origin.socket, server_side=True)
        threading.Thread(target=origin.serve_forever, daemon=True).start()
        udp_socket = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        udp_socket.bind(('127.0.0.1', 0)); udp_socket.settimeout(.2)
        stop = threading.Event()

        def respond():
            while not stop.is_set():
                try:
                    query, peer = udp_socket.recvfrom(4096)
                    if len(query) >= 17:
                        udp_socket.sendto(query[:2]+b'\x81\x80'+query[4:6]+b'\x00\x01\x00\x00\x00\x00'+query[12:]+bytes.fromhex('c00c000100010000000000047f000001'), peer)
                except socket.timeout:
                    pass
                except OSError:
                    break
        thread = threading.Thread(target=respond); thread.start()
        camouflage_port = f.port()
        camouflage = {'log': {'disabled': True}, 'inbounds': [{'type': 'anytls', 'listen': '127.0.0.1', 'listen_port': camouflage_port, 'users': [{'password': 'synthetic-only'}], 'tls': {'enabled': True, 'server_name': 'xhttp.example.test', 'min_version': '1.3', 'alpn': ['h2'], 'certificate': [cert], 'key': [key]}}], 'outbounds': [{'type': 'direct'}]}
        secret = x25519.X25519PrivateKey.generate()
        encode = lambda b: base64.urlsafe_b64encode(b).decode().rstrip('=')
        public = encode(secret.public_key().public_bytes(serialization.Encoding.Raw, serialization.PublicFormat.Raw))
        private = encode(secret.private_bytes(serialization.Encoding.Raw, serialization.PrivateFormat.Raw, serialization.NoEncryption()))
        cases = [(f'{security}-{version}-{mode}', security, version, mode, {}) for security, versions in [('tls', ['1.1', '2', '3']), ('reality', ['2'])] for version in versions for mode in ['stream-one', 'stream-up', 'packet-up', 'auto']]
        for placement in ['path', 'header', 'cookie', 'query']:
            cases.append(('metadata-'+placement, 'tls', '2', 'packet-up', {'sessionPlacement': placement, 'seqPlacement': placement, 'sessionKey': 'may-session', 'seqKey': 'may-seq'}))
        for placement in ['header', 'cookie']:
            cases.append(('payload-'+placement, 'tls', '2', 'packet-up', {'uplinkDataPlacement': placement, 'uplinkDataKey': 'May-Data', 'uplinkChunkSize': '512-1024', 'scMaxEachPostBytes': 4096}))
        for placement in ['header', 'cookie', 'query', 'queryInHeader']:
            for method in ['repeat-x', 'tokenish']:
                cases.append(('padding-'+placement+'-'+method, 'tls', '2', 'packet-up', {'xPaddingObfsMode': True, 'xPaddingPlacement': placement, 'xPaddingKey': 'may_padding', 'xPaddingHeader': 'X-May-Padding', 'xPaddingMethod': method, 'xPaddingBytes': '100-200'}))
        cases += [('query-escape', 'tls', '2', 'stream-up', {}), ('xmux-packets', 'tls', '2', 'packet-up', {'xmux': {'maxConcurrency': 2, 'cMaxReuseTimes': 3, 'hMaxRequestTimes': 3, 'hMaxReusableSecs': 3, 'hKeepAlivePeriod': -1}, 'scMaxEachPostBytes': 2048, 'scMinPostsIntervalMs': 5})]
        cases += [('get-packets', 'tls', '2', 'packet-up', {'uplinkHTTPMethod': 'GET', 'sessionPlacement': 'header', 'seqPlacement': 'query', 'uplinkDataPlacement': 'header', 'uplinkDataKey': 'May-Data', 'scMaxEachPostBytes': 2048}), ('put-packets', 'tls', '2', 'packet-up', {'uplinkHTTPMethod': 'PUT', 'noGRPCHeader': True, 'noSSEHeader': True, 'scMaxBufferedPosts': 4}), ('zero-defaults', 'tls', '2', 'packet-up', {'scMaxEachPostBytes': 0, 'scMinPostsIntervalMs': 0})]
        cases += [('padding-default-keys','tls','2','packet-up',{'xPaddingObfsMode':True}),('payload-default-key','tls','2','packet-up',{'uplinkDataPlacement':'header','scMaxEachPostBytes':2048})]
        cases += [('tls12-h2','tls','2','stream-one',{})]
        cases += [('large-padding','tls','2','packet-up',{'xPaddingObfsMode':True,'xPaddingPlacement':'header','xPaddingBytes':70000,'serverMaxHeaderBytes':262144})]
        cases += [('reality-uppercase-id','reality','2','auto',{}),('reality-empty-id','reality','2','auto',{})]
        if args.case:
            cases = [c for c in cases if args.case in c[0]]
        try:
            with runtime.start(args.core, camouflage, camouflage_port):
                for name, security, version, mode, extra in cases:
                    print('Checking', name, flush=True)
                    server_port, client_port = f.port(), f.port()
                    user = str(uuid.uuid4())
                    path = '/escaped%2Fsegment/?token=a%2Fb' if name == 'query-escape' else '/may-stream/'
                    server_extra = {k: v for k, v in extra.items() if k != 'xmux'}
                    stream = {'network': 'xhttp', 'security': security, 'xhttpSettings': {'path': path, 'host': 'front.example.test', 'mode': mode, 'extra': server_extra}}
                    alpn = {'1.1': 'http/1.1', '2': 'h2', '3': 'h3'}[version]
                    if security == 'tls':
                        stream['tlsSettings'] = {'minVersion': '1.3', 'alpn': [alpn], 'certificates': [{'certificate': cert.splitlines(), 'key': key.splitlines()}]}
                        if name=='tls12-h2':stream['tlsSettings'].update(minVersion='1.2',maxVersion='1.2')
                    else:
                        stream['realitySettings'] = {'target': f'127.0.0.1:{camouflage_port}', 'serverNames': ['xhttp.example.test'], 'privateKey': private, 'shortIds': ['' if name=='reality-empty-id' else 'abcd1234']}
                    server = {'log': {'loglevel': 'debug'}, 'inbounds': [{'listen': '127.0.0.1', 'port': server_port, 'protocol': 'vless', 'settings': {'decryption': 'none', 'clients': [{'id': user}]}, 'streamSettings': stream}], 'outbounds': [{'protocol': 'freedom'}]}
                    outbound = {'type': 'vless', 'tag': 'checked', 'server': '127.0.0.1', 'server_port': server_port, 'uuid': user, 'packet_encoding': 'xudp', 'tls': {'enabled': True, 'server_name': 'xhttp.example.test', 'alpn': [alpn]}, 'transport': {'type': 'xhttp', 'mode': mode, 'path': path, 'host': 'front.example.test', 'http_version': version, 'extra': extra}}
                    if security == 'tls':
                        outbound['tls']['certificate'] = [ca]
                        if name=='tls12-h2':outbound['tls'].update(min_version='1.2',max_version='1.2')
                    else:
                        outbound['tls'].update(utls={'enabled': True, 'fingerprint': 'chrome'}, reality={'enabled': True, 'public_key': public, 'short_id': '' if name=='reality-empty-id' else 'ABCD1234' if name=='reality-uppercase-id' else 'abcd1234'})
                    client = {'log': {'level': 'debug'}, 'inbounds': [{'type': 'mixed', 'listen': '127.0.0.1', 'listen_port': client_port}], 'outbounds': [outbound], 'route': {'final': 'checked'}}
                    started = time.monotonic()
                    try:
                        with runtime.start(args.xray, server, server_port, True, udp_only=version == '3'), runtime.start(args.core, client, client_port):
                            assert f.https(client_port, origin.server_port, ca) == b'198.51.100.22'
                            f.udp(client_port, udp_socket.getsockname()[1])
                            f.https_upload(client_port, origin.server_port, ca, b'may-upload'*32768)
                            with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
                                data = list(pool.map(lambda _: f.https(client_port, origin.server_port, ca, '/payload'), range(4)))
                            assert all(d == b'xhttp-payload'*16384 for d in data)
                        result['cases'].append({'case': name, 'https': True, 'application_udp': True, 'parallel_large_response': 4, 'seconds': round(time.monotonic()-started, 3)})
                    except Exception as error:
                        result['failures'].append({'case': name, 'error': type(error).__name__+': '+str(error)})
                        print('FAILED', name, type(error).__name__, str(error), flush=True)
                        if not args.continue_on_error:
                            raise
                if args.proxy and (not args.case or args.case == 'split'):
                    for mode, up_version, down_version in [('stream-up','2','2'),('packet-up','3','2'),('auto','2','3')]:
                        name=f'split-{mode}-{up_version}-{down_version}'
                        print('Checking', name, flush=True)
                        backend, upload, download, local = f.port(), f.port(), f.port(), f.port()
                        user=str(uuid.uuid4()); token=uuid.uuid4().hex
                        up_extra={'sessionPlacement':'header','sessionKey':'X-Up-Session','seqPlacement':'header','seqKey':'X-Up-Seq'}
                        down_extra={'sessionPlacement':'cookie','sessionKey':'down_sid','xmux':{'hMaxRequestTimes':2,'maxConnections':2}}
                        stream={'network':'xhttp','security':'tls','tlsSettings':{'minVersion':'1.3','alpn':['h2'],'certificates':[{'certificate':cert.splitlines(),'key':key.splitlines()}]},'xhttpSettings':{'host':'backend.example.test','path':'/backend/','mode':'auto','extra':up_extra}}
                        server={'log':{'loglevel':'debug'},'inbounds':[{'listen':'127.0.0.1','port':backend,'protocol':'vless','settings':{'decryption':'none','clients':[{'id':user}]},'streamSettings':stream}],'outbounds':[{'protocol':'freedom'}]}
                        proxy={'Certificate':cert,'Key':key,'CA':ca,'Backend':f'127.0.0.1:{backend}','BackendHost':'backend.example.test','BackendPath':'/backend/','Token':token,'Endpoints':[{'Port':upload,'Host':'upload.example.test','Path':'/upload/','HTTPVersion':up_version},{'Port':download,'Host':'download.example.test','Path':'/download/','HTTPVersion':down_version,'SessionCookie':'down_sid'}]}
                        def security(version):
                            return {'enabled':True,'server_name':'xhttp.example.test','alpn':[{'1.1':'http/1.1','2':'h2','3':'h3'}[version]],'certificate':[ca]}
                        outbound={'type':'vless','tag':'checked','server':'127.0.0.1','server_port':upload,'uuid':user,'tls':security(up_version),'transport':{'type':'xhttp','path':'/upload/','host':'upload.example.test','mode':mode,'http_version':up_version,'extra':up_extra,'download_settings':{'server':'127.0.0.1','server_port':download,'tls':security(down_version),'transport':{'type':'xhttp','path':'/download/','host':'download.example.test','http_version':down_version,'extra':down_extra}}}}
                        client={'log':{'level':'debug'},'inbounds':[{'type':'mixed','listen':'127.0.0.1','listen_port':local}],'outbounds':[outbound],'route':{'final':'checked'}}
                        try:
                            with runtime.start(args.xray,server,backend,True),runtime.start(args.proxy,proxy,upload,True,udp_only=up_version=='3'),runtime.start(args.core,client,local):
                                assert f.https(local,origin.server_port,ca)==b'198.51.100.22'
                                f.https_upload(local,origin.server_port,ca,b'split-upload'*32768)
                                f.udp(local,udp_socket.getsockname()[1])
                                # Use the HTTP/2 side for private fixture controls.
                                control_port=upload if up_version=='2' else download
                                def control(path):
                                    conn=f.http.client.HTTPSConnection('127.0.0.1',control_port,context=ssl.create_default_context(cadata=ca),timeout=5)
                                    try:conn.request('GET',path,headers={'X-Fixture-Token':token});response=conn.getresponse();assert response.status==200;return json.loads(response.read())
                                    finally:conn.close()
                                counts=control('/__stats');assert counts.get('upload.example.test',0)>0 and counts.get('download.example.test',0)>0,'one side bypassed the reverse proxy'
                                if down_version=='2':
                                    control('/__fail?enabled=1');before=f.Origin.count
                                    try:f.https(local,origin.server_port,ca)
                                    except (OSError,f.http.client.HTTPException):pass
                                    else:raise AssertionError('failed download used fallback')
                                    assert f.Origin.count==before
                                    control('/__fail?enabled=0');assert f.https(local,origin.server_port,ca)==b'198.51.100.22'
                            result['cases'].append({'case':name,'https':True,'large_upload':True,'application_udp':True,'both_proxy_endpoints_observed':True,'download_failure_reconnect':down_version=='2'})
                        except Exception as error:
                            result['failures'].append({'case':name,'error':type(error).__name__+': '+str(error)})
                            if not args.continue_on_error:raise
                if args.proxy and (not args.case or args.case in ('split','split-security')):
                    for up_security,down_security in [('reality','reality'),('tls','reality'),('reality','tls')]:
                        name=f'split-security-{up_security}-{down_security}';print('Checking',name,flush=True)
                        backend,front,local=f.port(),f.port(),f.port();user=str(uuid.uuid4());token=uuid.uuid4().hex
                        extra={'sessionPlacement':'header','sessionKey':'X-Up-Session','seqPlacement':'header','seqKey':'X-Up-Seq'}
                        stream={'network':'xhttp','security':'reality','realitySettings':{'target':f'127.0.0.1:{camouflage_port}','serverNames':['xhttp.example.test','up.example.test','down.example.test'],'privateKey':private,'shortIds':['abcd1234']},'xhttpSettings':{'host':'backend.example.test','path':'/backend/','mode':'auto','extra':extra}}
                        server={'log':{'loglevel':'debug'},'inbounds':[{'listen':'127.0.0.1','port':backend,'protocol':'vless','settings':{'decryption':'none','clients':[{'id':user}]},'streamSettings':stream}],'outbounds':[{'protocol':'freedom'}]}
                        proxy={'Certificate':cert,'Key':key,'CA':ca,'Backend':f'127.0.0.1:{backend}','BackendHost':'backend.example.test','BackendPath':'/backend/','BackendRealityPublicKey':public,'BackendRealityShortID':'abcd1234','Token':token,'Endpoints':[{'Port':front,'Host':'front.example.test','Path':'/front/','HTTPVersion':'2'}]}
                        def identity(security,sni):
                            value={'enabled':True,'server_name':sni,'alpn':['h2']}
                            if security=='tls':value['certificate']=[ca]
                            else:value.update(utls={'enabled':True,'fingerprint':'chrome'},reality={'enabled':True,'public_key':public,'short_id':'abcd1234'})
                            return value
                        try:
                            with runtime.start(args.xray,server,backend,True),runtime.start(args.proxy,proxy,front,True),relay(backend) as up,relay(backend) as down:
                                def transport(security):return {'type':'xhttp','path':'/front/' if security=='tls' else '/backend/','host':'front.example.test' if security=='tls' else 'backend.example.test','http_version':'2','extra':extra}
                                up_transport=transport(up_security);up_transport['mode']='auto';up_transport['download_settings']={'server':'127.0.0.1','server_port':front if down_security=='tls' else down.server_address[1],'tls':identity(down_security,'xhttp.example.test' if down_security=='tls' else 'down.example.test'),'transport':transport(down_security)}
                                outbound={'type':'vless','tag':'checked','server':'127.0.0.1','server_port':front if up_security=='tls' else up.server_address[1],'uuid':user,'tls':identity(up_security,'xhttp.example.test' if up_security=='tls' else 'up.example.test'),'transport':up_transport}
                                client={'log':{'level':'debug'},'inbounds':[{'type':'mixed','listen':'127.0.0.1','listen_port':local}],'outbounds':[outbound],'route':{'final':'checked'}}
                                with runtime.start(args.core,client,local):
                                    assert f.https(local,origin.server_port,ca)==b'198.51.100.22';f.https_upload(local,origin.server_port,ca,b'identity-split'*32768);f.udp(local,udp_socket.getsockname()[1])
                                    broken=json.loads(json.dumps(client));broken_port=f.port();broken['inbounds'][0]['listen_port']=broken_port;down_tls=broken['outbounds'][0]['transport']['download_settings']['tls']
                                    if down_security=='tls':down_tls['server_name']='wrong.example.test'
                                    else:down_tls['reality']['public_key']=private
                                    before=f.Origin.count
                                    with runtime.start(args.core,broken,broken_port):
                                        try:f.https(broken_port,origin.server_port,ca)
                                        except (OSError,f.http.client.HTTPException):pass
                                        else:raise AssertionError('Invalid independent download identity was accepted')
                                    assert f.Origin.count==before
                                    assert f.https(local,origin.server_port,ca)==b'198.51.100.22'
                                assert up_security!='reality' or up.forwarded>0
                                assert down_security!='reality' or down.forwarded>0
                            result['cases'].append({'case':name,'https':True,'large_upload':True,'application_udp':True,'independent_download_identity_negative':True,'distinct_addresses_observed':True})
                        except Exception as error:
                            result['failures'].append({'case':name,'error':type(error).__name__+': '+str(error)})
                            if not args.continue_on_error:raise
        finally:
            stop.set(); thread.join(2); udp_socket.close(); origin.shutdown(); origin.server_close()
            for child in runtime.children:
                if child.poll() is None:
                    child.kill(); child.wait(5)
            result['owned_children_stopped'] = all(c.poll() is not None for c in runtime.children)
            args.output.write_text(json.dumps(result, indent=2), encoding='utf-8')
    assert not result['failures'], 'Matrix includes failed combinations; not an acceptance pass'
    print('PASS', len(result['cases']), 'real XHTTP combinations; all owned children closed')


if __name__ == '__main__':
    p = argparse.ArgumentParser()
    p.add_argument('--core', type=Path, required=True)
    p.add_argument('--xray', type=Path, required=True)
    p.add_argument('--output', type=Path, required=True)
    p.add_argument('--case')
    p.add_argument('--continue-on-error', action='store_true')
    p.add_argument('--proxy', type=Path)
    run(p.parse_args())
