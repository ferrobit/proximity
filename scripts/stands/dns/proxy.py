# HTTP proxy of the stand "dns": CONNECT only, prints every tunnel.
#   python3 proxy.py <address> <port> [<name>=<ip> ...]
# Log line: "CONNECT <target> from=<client>".
import socket
import sys
import threading

hosts = dict(item.split("=", 1) for item in sys.argv[3:])


def pipe(source, target):
    try:
        while data := source.recv(65536):
            target.sendall(data)
    except OSError:
        pass
    finally:
        target.close()


def handle(client, address):
    try:
        request = b""
        while b"\r\n\r\n" not in request:
            chunk = client.recv(4096)
            if not chunk:
                return client.close()
            request += chunk
        method, target = request.split(b" ")[:2]
        print(f"{method.decode()} {target.decode()} from={address[0]}", flush=True)
        if method != b"CONNECT":
            client.sendall(b"HTTP/1.1 405 Method Not Allowed\r\n\r\n")
            return client.close()
        host, port = target.decode().rsplit(":", 1)
        upstream = socket.create_connection((hosts.get(host, host), int(port)), timeout=5)
        client.sendall(b"HTTP/1.1 200 Connection established\r\n\r\n")
        threading.Thread(target=pipe, args=(client, upstream), daemon=True).start()
        pipe(upstream, client)
    except OSError:
        client.close()


server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
server.bind((sys.argv[1], int(sys.argv[2])))
server.listen(16)
while True:
    connection, peer = server.accept()
    threading.Thread(target=handle, args=(connection, peer), daemon=True).start()
