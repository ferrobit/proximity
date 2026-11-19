# Minimal DNS server of the stand "dns": answers A queries and prints who asked.
#   python3 server.py <address>[,<address>...] [<name>[@<address>]=<ip> ...]
# Listens on port 53 (UDP and TCP) of every given address: a server bound to one address
# answers from the address that was asked. With /tls/cert.pem and key.pem it also
# serves DNS over TLS (853) and DNS over HTTPS (443, GET and POST /dns-query). A name from
# the arguments (or its subdomain) gets its address, only on the given address with "@";
# every other name gets DEFAULT; other record types get an empty answer.
# Log line: "<type> <name> from=<client> to=<asked address> <udp|tcp|tls|https>".
import base64
import http.server
import os
import socket
import ssl
import struct
import sys
import threading

DEFAULT = "1.1.1.2"
CERTIFICATE = "/tls/cert.pem"
KEY = "/tls/key.pem"
hosts = [(item.split("=", 1)[0].partition("@"), item.split("=", 1)[1]) for item in sys.argv[2:]]


def answer(packet, client, asked, transport):
    labels, offset = [], 12
    while packet[offset]:
        size = packet[offset]
        labels.append(packet[offset + 1:offset + 1 + size].decode("ascii", "replace").lower())
        offset += size + 1
    kind = struct.unpack("!H", packet[offset + 1:offset + 3])[0]
    name = ".".join(labels)
    print(f"{'A' if kind == 1 else kind} {name} from={client} to={asked} {transport}", flush=True)
    address = DEFAULT
    for (host, _, only), value in hosts:
        if (name == host or name.endswith("." + host)) and only in ("", asked):
            address = value
    reply = packet[:2] + b"\x81\x80" + packet[4:6] + (b"\0\1" if kind == 1 else b"\0\0") + b"\0\0\0\0"
    reply += packet[12:offset + 5]
    if kind == 1:
        reply += b"\xc0\x0c\0\1\0\1\0\0\0\5\0\4" + socket.inet_aton(address)
    return reply


def serve_datagrams(asked):
    server = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    server.bind((asked, 53))
    while True:
        packet, client = server.recvfrom(4096)
        try:
            server.sendto(answer(packet, client[0], asked, "udp"), client)
        except (IndexError, struct.error):
            continue


def read_exact(connection, size):
    data = b""
    while len(data) < size:
        chunk = connection.recv(size - len(data))
        if not chunk:
            raise ConnectionError("closed")
        data += chunk
    return data


def serve_stream(connection, client, asked, transport="tcp"):
    try:
        while True:
            size = struct.unpack("!H", read_exact(connection, 2))[0]
            reply = answer(read_exact(connection, size), client[0], asked, transport)
            connection.sendall(struct.pack("!H", len(reply)) + reply)
    except (ConnectionError, IndexError, struct.error, OSError, ssl.SSLError):
        connection.close()


def serve_streams(asked):
    server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server.bind((asked, 53))
    server.listen(16)
    while True:
        connection, client = server.accept()
        threading.Thread(target=serve_stream, args=(connection, client, asked), daemon=True).start()


def serve_secure(asked, context):
    server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server.bind((asked, 853))
    server.listen(16)
    while True:
        connection, client = server.accept()
        try:
            connection = context.wrap_socket(connection, server_side=True)
        except (ssl.SSLError, OSError):
            connection.close()
            continue
        threading.Thread(target=serve_stream, args=(connection, client, asked, "tls"),
                         daemon=True).start()


def serve_https(asked, context):
    class Handler(http.server.BaseHTTPRequestHandler):
        def reply(self, packet):
            data = answer(packet, self.client_address[0], asked, "https")
            self.send_response(200)
            self.send_header("Content-Type", "application/dns-message")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def do_GET(self):
            query = self.path.partition("dns=")[2].split("&")[0]
            self.reply(base64.urlsafe_b64decode(query + "=" * (-len(query) % 4)))

        def do_POST(self):
            self.reply(self.rfile.read(int(self.headers["Content-Length"])))

        def log_message(self, *arguments):
            pass

    server = http.server.ThreadingHTTPServer((asked, 443), Handler)
    server.socket = context.wrap_socket(server.socket, server_side=True)
    server.serve_forever()


secure = None
if os.path.exists(CERTIFICATE):
    secure = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    secure.load_cert_chain(CERTIFICATE, KEY)
for asked in sys.argv[1].split(","):
    threading.Thread(target=serve_datagrams, args=(asked,), daemon=True).start()
    threading.Thread(target=serve_streams, args=(asked,), daemon=True).start()
    if secure:
        threading.Thread(target=serve_secure, args=(asked, secure), daemon=True).start()
        threading.Thread(target=serve_https, args=(asked, secure), daemon=True).start()
threading.Event().wait()
