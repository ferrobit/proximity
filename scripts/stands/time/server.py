# Minimal NTP server for a stand without Internet: answers every request with the clock
# of the container. Started by scripts/stands/lib.sh when STAND_OFFLINE=1.
import socket
import struct
import time

EPOCH = 2208988800

server = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
server.bind(("0.0.0.0", 123))
while True:
    data, address = server.recvfrom(128)
    if len(data) < 48:
        continue
    now = time.time() + EPOCH
    stamp = struct.pack("!II", int(now), int((now % 1) * (1 << 32)))
    header = bytes([0x24, 1, 0, 0xEC]) + b"\0" * 8 + b"LOCL"
    server.sendto(header + stamp + data[40:48] + stamp + stamp, address)
