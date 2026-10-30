"""Windows loopback acceptance for May XHTTP; never loads user proxy profiles.

Requires cryptography and psutil. Supply locally built candidate/baseline cores
and the pinned Xray 26.3.27 Windows binary. All listeners use loopback, all
credentials/PKI are generated, and every owned child is closed in finally.
"""
import argparse
import base64
import concurrent.futures
import contextlib
import datetime
import hashlib
import http.client
import http.server
import json
import os
from pathlib import Path
import socket
import ssl
import statistics
import struct
import subprocess
import tempfile
import threading
import time
import uuid

import psutil
from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import rsa, x25519
from cryptography.x509.oid import NameOID, ExtendedKeyUsageOID
import ipaddress

XRAY_SHA256 = '15c2d007954ac53ba69b80ec91242786b3c0b71d52649165b4ca1d5cc96ef8f1'


def port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


def receive(s, n):
    value = b''
    while len(value) < n:
        part = s.recv(n-len(value))
        if not part:
            raise OSError('Synthetic peer closed')
        value += part
    return value


def address(s, atyp):
    size = {1: 4, 4: 16}.get(atyp)
    if size is None:
        size = receive(s, 1)[0]
    raw = receive(s, size)
    value = socket.inet_ntop(socket.AF_INET if atyp == 1 else socket.AF_INET6, raw) if atyp in (1, 4) else raw.decode()
    return value, struct.unpack('!H', receive(s, 2))[0]


class Runtime:
    def __init__(self, directory):
        self.directory = Path(directory)
        self.children = []
        self.serial = 0

    @contextlib.contextmanager
    def start(self, binary, config, listener, xray=False, udp_only=False):
        self.serial += 1
        filename = self.directory / f'config-{self.serial}.json'
        filename.write_text(json.dumps(config), encoding='utf-8')
        log = (self.directory / f'log-{self.serial}.txt').open('w', encoding='utf-8')
        flags = subprocess.CREATE_NO_WINDOW if os.name == 'nt' else 0
        process = subprocess.Popen([str(binary), 'run', '-config' if xray else '-c', str(filename)], stdout=log, stderr=log, creationflags=flags)
        self.children.append(process)
        try:
            for _ in range(100):
                if process.poll() is not None:
                    raise RuntimeError('Synthetic core exited during startup')
                if udp_only:
                    time.sleep(.3)
                    if process.poll() is None:
                        break
                try:
                    with socket.create_connection(('127.0.0.1', listener), .1):
                        break
                except OSError:
                    time.sleep(.05)
            else:
                raise RuntimeError('Synthetic core did not become ready')
            yield process
        except BaseException:
            log.flush()
            (self.directory.parent / f'failure-core-{self.serial}.log').write_text((self.directory / f'log-{self.serial}.txt').read_text(encoding='utf-8'), encoding='utf-8')
            raise
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(5)
            log.close()


def pki(directory):
    now = datetime.datetime.now(datetime.timezone.utc)
    root_key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    name = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, 'May isolated XHTTP CA')])
    root = x509.CertificateBuilder().subject_name(name).issuer_name(name).public_key(root_key.public_key()).serial_number(x509.random_serial_number()).not_valid_before(now-datetime.timedelta(minutes=5)).not_valid_after(now+datetime.timedelta(days=2)).add_extension(x509.BasicConstraints(ca=True, path_length=None), critical=True).add_extension(x509.SubjectKeyIdentifier.from_public_key(root_key.public_key()), critical=False).add_extension(x509.KeyUsage(True, False, False, False, False, True, True, None, None), critical=True).sign(root_key, hashes.SHA256())
    key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    leaf = x509.CertificateBuilder().subject_name(x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, 'xhttp.example.test')])).issuer_name(name).public_key(key.public_key()).serial_number(x509.random_serial_number()).not_valid_before(now-datetime.timedelta(minutes=3)).not_valid_after(now+datetime.timedelta(days=1)).add_extension(x509.SubjectAlternativeName([x509.DNSName('xhttp.example.test'), x509.IPAddress(ipaddress.ip_address('127.0.0.1'))]), critical=False).add_extension(x509.BasicConstraints(ca=False, path_length=None), critical=True).add_extension(x509.SubjectKeyIdentifier.from_public_key(key.public_key()), critical=False).add_extension(x509.AuthorityKeyIdentifier.from_issuer_public_key(root_key.public_key()), critical=False).add_extension(x509.ExtendedKeyUsage([ExtendedKeyUsageOID.SERVER_AUTH]), critical=False).add_extension(x509.KeyUsage(True, False, True, False, False, False, False, None, None), critical=True).sign(root_key, hashes.SHA256())
    ca = root.public_bytes(serialization.Encoding.PEM).decode()
    cert = leaf.public_bytes(serialization.Encoding.PEM).decode() + ca
    private = key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption()).decode()
    (directory/'cert.pem').write_text(cert, encoding='ascii')
    (directory/'key.pem').write_text(private, encoding='ascii')
    return ca, cert, private


class Origin(http.server.BaseHTTPRequestHandler):
    count = 0
    lock = threading.Lock()

    def do_GET(self):
        with self.lock:
            Origin.count += 1
        data = b'198.51.100.22' if self.path == '/' else b'xhttp-payload' * 16384
        self.send_response(200)
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        try:
            self.wfile.write(data)
        except OSError:
            pass

    def log_message(self, *_):
        pass

    def do_POST(self):
        data = self.rfile.read(int(self.headers.get('Content-Length', '0')))
        payload = hashlib.sha256(data).hexdigest().encode()
        self.send_response(200); self.send_header('Content-Length', str(len(payload))); self.end_headers(); self.wfile.write(payload)


def https(proxy, target, ca, path='/'):
    context = ssl.create_default_context(cadata=ca)
    connection = http.client.HTTPSConnection('127.0.0.1', proxy, context=context, timeout=3)
    connection.set_tunnel('127.0.0.1', target)
    try:
        connection.request('GET', path)
        response = connection.getresponse()
        data = response.read()
        if response.status != 200:
            raise OSError('Synthetic HTTPS status failed')
        return data
    finally:
        connection.close()


def https_upload(proxy, target, ca, payload):
    connection = http.client.HTTPSConnection('127.0.0.1', proxy, context=ssl.create_default_context(cadata=ca), timeout=10)
    connection.set_tunnel('127.0.0.1', target)
    try:
        connection.request('POST', '/upload', payload)
        response = connection.getresponse()
        assert response.status == 200 and response.read() == hashlib.sha256(payload).hexdigest().encode(), 'Upload altered or truncated'
    finally:
        connection.close()


def udp(proxy, target):
    with socket.create_connection(('127.0.0.1', proxy), 3) as control, socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
        control.sendall(b'\x05\x01\x00')
        assert receive(control, 2) == b'\x05\x00', 'SOCKS authentication failed'
        control.sendall(b'\x05\x03\x00\x01' + b'\x00'*6)
        header = receive(control, 4)
        assert header[:3] == b'\x05\x00\x00', 'UDP associate failed'
        host, relay = address(control, header[3])
        query = os.urandom(2) + b'\x01\x00\x00\x01' + b'\x00'*6 + b'\x05probe\x04test\x00\x00\x01\x00\x01'
        sock.settimeout(3)
        sock.sendto(b'\x00\x00\x00\x01\x7f\x00\x00\x01' + struct.pack('!H', target) + query, ('127.0.0.1' if host == '0.0.0.0' else host, relay))
        data, _ = sock.recvfrom(4096)
        assert data[:4] == b'\x00\x00\x00\x01' and data[4:8] == b'\x7f\x00\x00\x01' and data[8:10] == struct.pack('!H', target), 'UDP target changed'
        answer = data[10:]
        assert answer[:2] == query[:2] and answer[12:len(query)] == query[12:] and answer[-4:] == b'\x7f\x00\x00\x01', 'UDP question or answer changed'


def idle(runtime, binary):
    listener = port()
    config = {'log': {'disabled': True}, 'inbounds': [{'type': 'mixed', 'listen': '127.0.0.1', 'listen_port': listener}], 'outbounds': [{'type': 'direct', 'tag': 'egress'}], 'route': {'final': 'egress'}}
    with runtime.start(binary, config, listener) as process:
        child = psutil.Process(process.pid)
        time.sleep(3)
        before = child.cpu_times()
        start = time.monotonic()
        samples = []
        for _ in range(20):
            info = child.memory_info()
            samples.append((info.rss, getattr(info, 'private', info.vms)))
            time.sleep(.25)
        after = child.cpu_times()
        def stats(index):
            values = sorted(s[index]/1024/1024 for s in samples)
            return {'median_MiB': round(statistics.median(values), 3), 'p95_MiB': round(values[int((len(values)-1)*.95)], 3), 'peak_MiB': round(max(values), 3)}
        return {'rss': stats(0), 'private': stats(1), 'cpu_one_core_percent': round(100*(after.user+after.system-before.user-before.system)/(time.monotonic()-start), 3), 'seconds': 5, 'warmup_seconds': 3}


def run(args):
    assert os.name == 'nt', 'This pinned executable fixture targets Windows'
    assert hashlib.sha256(args.xray.read_bytes()).hexdigest() == XRAY_SHA256, 'Pinned Xray binary differs'
    results = {'scope': 'Windows loopback only; synthetic PKI; no system proxy/TUN or service installation', 'core_sha256': hashlib.sha256(args.core.read_bytes()).hexdigest(), 'baseline_sha256': hashlib.sha256(args.baseline.read_bytes()).hexdigest(), 'xray_sha256': XRAY_SHA256, 'cases': []}
    with tempfile.TemporaryDirectory(prefix='may-xhttp-', dir=args.output.parent) as temporary:
        directory = Path(temporary)
        runtime = Runtime(directory)
        ca, cert, private = pki(directory)
        origin = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Origin)
        origin.daemon_threads = True
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(str(directory/'cert.pem'), str(directory/'key.pem'))
        origin.socket = context.wrap_socket(origin.socket, server_side=True)
        threading.Thread(target=origin.serve_forever, daemon=True).start()
        dns = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        dns.bind(('127.0.0.1', 0))
        dns.settimeout(.2)
        stop = threading.Event()
        def respond():
            while not stop.is_set():
                try:
                    query, peer = dns.recvfrom(4096)
                    if len(query) >= 17:
                        dns.sendto(query[:2]+b'\x81\x80'+query[4:6]+b'\x00\x01\x00\x00\x00\x00'+query[12:]+bytes.fromhex('c00c000100010000000000047f000001'), peer)
                except socket.timeout:
                    pass
                except OSError:
                    break
        thread = threading.Thread(target=respond)
        thread.start()
        try:
            camouflage_port = port()
            camouflage = {'log': {'disabled': True}, 'inbounds': [{'type': 'anytls', 'listen': '127.0.0.1', 'listen_port': camouflage_port, 'users': [{'password': 'synthetic-only'}], 'tls': {'enabled': True, 'server_name': 'xhttp.example.test', 'min_version': '1.3', 'alpn': ['h2'], 'certificate': [cert], 'key': [private]}}], 'outbounds': [{'type': 'direct'}]}
            secret = x25519.X25519PrivateKey.generate()
            encode = lambda b: base64.urlsafe_b64encode(b).decode().rstrip('=')
            public_key = encode(secret.public_key().public_bytes(serialization.Encoding.Raw, serialization.PublicFormat.Raw))
            private_key = encode(secret.private_bytes(serialization.Encoding.Raw, serialization.PrivateFormat.Raw, serialization.NoEncryption()))
            with runtime.start(args.baseline, camouflage, camouflage_port):
                for security in ('tls', 'reality', 'vision', 'anytls'):
                    print('Testing synthetic', security, flush=True)
                    server_port = camouflage_port if security == 'anytls' else port()
                    users = [str(uuid.uuid4()), str(uuid.uuid4())]
                    if security == 'tls':
                        stream = {'network': 'xhttp', 'security': 'tls', 'xhttpSettings': {'path': '/may-stream/', 'host': 'front.example.test', 'mode': 'stream-one'}, 'tlsSettings': {'minVersion': '1.3', 'maxVersion': '1.3', 'alpn': ['h2'], 'certificates': [{'certificate': cert.splitlines(), 'key': private.splitlines()}]}}
                    else:
                        stream = {'network': 'tcp' if security == 'vision' else 'xhttp', 'security': 'reality', 'realitySettings': {'show': False, 'target': f'127.0.0.1:{camouflage_port}', 'serverNames': ['xhttp.example.test'], 'privateKey': private_key, 'shortIds': ['abcd1234'], 'xver': 0}}
                        if security == 'reality':
                            stream['xhttpSettings'] = {'path': '/may-stream/', 'host': 'front.example.test', 'mode': 'stream-one'}
                    config = {'log': {'loglevel': 'none'}, 'inbounds': [{'listen': '127.0.0.1', 'port': server_port, 'protocol': 'vless', 'settings': {'decryption': 'none', 'clients': [dict(id=u, **({'flow': 'xtls-rprx-vision'} if security == 'vision' else {})) for u in users]}, 'streamSettings': stream}], 'outbounds': [{'protocol': 'freedom'}]}
                    server = runtime.start(args.xray, config, server_port, True) if security != 'anytls' else contextlib.nullcontext()
                    with server:
                        for user in users[:2 if security in ('tls', 'reality') else 1]:
                            client_port = port()
                            outbound = {'type': 'vless', 'tag': 'checked', 'server': '127.0.0.1', 'server_port': server_port, 'uuid': user, 'packet_encoding': 'xudp', 'tls': {'enabled': True, 'server_name': 'xhttp.example.test', 'min_version': '1.3', 'alpn': ['h2']}}
                            if security in ('tls', 'anytls'):
                                outbound['tls']['certificate'] = [ca]
                            else:
                                outbound['tls'].update(utls={'enabled': True, 'fingerprint': 'chrome'}, reality={'enabled': True, 'public_key': public_key, 'short_id': 'abcd1234'})
                            if security in ('tls', 'reality'):
                                outbound['transport'] = {'type': 'xhttp', 'path': '/may-stream/', 'host': 'front.example.test', 'mode': 'stream-one'}
                            if security == 'vision':
                                outbound['flow'] = 'xtls-rprx-vision'
                            if security == 'anytls':
                                outbound['type'] = 'anytls'
                                outbound['password'] = 'synthetic-only'
                                outbound.pop('uuid')
                                outbound.pop('packet_encoding')
                            candidate = {'log': {'level': 'debug'}, 'inbounds': [{'type': 'mixed', 'listen': '127.0.0.1', 'listen_port': client_port}], 'outbounds': [outbound], 'route': {'final': 'checked'}}
                            with runtime.start(args.core, candidate, client_port):
                                assert https(client_port, origin.server_port, ca) == b'198.51.100.22', f'{security} HTTPS failed'
                                udp(client_port, dns.getsockname()[1])
                                if security in ('tls', 'reality'):
                                    with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
                                        values = list(pool.map(lambda _: https(client_port, origin.server_port, ca, '/payload'), range(16)))
                                    assert all(v == b'xhttp-payload'*16384 for v in values), 'Concurrent response corrupted'
                            results['cases'].append({'security': security, 'https': True, 'application_udp': True, 'concurrent_streams': security in ('tls', 'reality')})
                        if security in ('tls', 'reality'):
                            for fault in ('uuid', 'sni', 'path', 'host') + (('public-key', 'short-id') if security == 'reality' else ()):
                                broken = json.loads(json.dumps(candidate))
                                item = broken['outbounds'][0]
                                if fault == 'uuid': item['uuid'] = str(uuid.uuid4())
                                if fault == 'sni': item['tls']['server_name'] = 'wrong.example.test'
                                if fault == 'path': item['transport']['path'] = '/wrong/'
                                if fault == 'host': item['transport']['host'] = 'wrong.example.test'
                                if fault == 'public-key': item['tls']['reality']['public_key'] = private_key
                                if fault == 'short-id': item['tls']['reality']['short_id'] = 'deadbeef'
                                client_port = port()
                                broken['inbounds'][0]['listen_port'] = client_port
                                before = Origin.count
                                with runtime.start(args.core, broken, client_port):
                                    try:
                                        https(client_port, origin.server_port, ca)
                                    except (OSError, ssl.SSLError, http.client.HTTPException):
                                        pass
                                    else:
                                        raise AssertionError(f'{security} invalid {fault} reached origin')
                                assert Origin.count == before, 'Invalid connection used a direct fallback'
                            results['cases'].append({'security': security, 'negative_cases': 6 if security == 'reality' else 4, 'no_fallback': True})
            results['idle_baseline'] = idle(runtime, args.baseline)
            results['idle_candidate'] = idle(runtime, args.core)
            results['idle_scope'] = 'Same Go toolchain, build tags, unchanged direct/mixed config; serial short samples; not long-run or Electron memory'
        finally:
            stop.set()
            thread.join(2)
            dns.close()
            origin.shutdown()
            origin.server_close()
            for child in runtime.children:
                if child.poll() is None:
                    child.kill()
                    child.wait(5)
            results['owned_children_stopped'] = all(p.poll() is not None for p in runtime.children)
    args.output.write_text(json.dumps(results, indent=2), encoding='utf-8')
    print('PASS: TLS/Reality XHTTP, two users each, HTTPS, application UDP, concurrency, ten negative cases; legacy Vision/AnyTLS; owned children closed')


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--core', type=Path, required=True)
    parser.add_argument('--baseline', type=Path, required=True)
    parser.add_argument('--xray', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    run(parser.parse_args())
