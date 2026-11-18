# Minimal DNS server of the stand "dns": answers A queries and prints who asked.
#   python3 server.py <address>[,<address>...] [<name>=<ip> ...]
# Listens on port 53 (UDP and TCP) of every given address: a server bound to one address
# answers from the address that was asked. A name from the arguments (or its subdomain)
# gets its address, every other name gets DEFAULT; other record types get an empty answer.
# Log line: "<type> <name> from=<client> to=<asked address> <udp|tcp>".
import socket
import struct
import sys
import threading

DEFAULT = "1.1.1.2"
hosts = dict(item.split("=", 1) for item in sys.argv[2:])


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
    for host, value in hosts.items():
        if name == host or name.endswith("." + host):
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


def serve_stream(connection, client, asked):
    try:
        while True:
            size = struct.unpack("!H", read_exact(connection, 2))[0]
            reply = answer(read_exact(connection, size), client[0], asked, "tcp")
            connection.sendall(struct.pack("!H", len(reply)) + reply)
    except (ConnectionError, IndexError, struct.error, OSError):
        connection.close()


def serve_streams(asked):
    server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server.bind((asked, 53))
    server.listen(16)
    while True:
        connection, client = server.accept()
        threading.Thread(target=serve_stream, args=(connection, client, asked), daemon=True).start()


for asked in sys.argv[1].split(","):
    threading.Thread(target=serve_datagrams, args=(asked,), daemon=True).start()
    threading.Thread(target=serve_streams, args=(asked,), daemon=True).start()
threading.Event().wait()
