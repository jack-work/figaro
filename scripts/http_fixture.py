"""Read request bodies for the local gateway fixtures."""


def request_body(handler):
    if handler.headers.get("Transfer-Encoding", "").lower() != "chunked":
        return handler.rfile.read(int(handler.headers.get("Content-Length", 0)))

    chunks = []
    while True:
        line = handler.rfile.readline(8192)
        size = int(line.split(b";", 1)[0].strip(), 16)
        if size == 0:
            while True:
                trailer = handler.rfile.readline(8192)
                if trailer in (b"\r\n", b"\n", b""):
                    return b"".join(chunks)
        chunk = handler.rfile.read(size)
        if len(chunk) != size or handler.rfile.read(2) != b"\r\n":
            raise ValueError("incomplete chunked request")
        chunks.append(chunk)
