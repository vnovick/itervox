#!/usr/bin/env python3
"""Stand-in for `ssh` and `gcloud` in the remote-connection tests (#156).

Installed as `ssh` and/or `gcloud` in a directory of its own, it treats the
"remote host" as this machine:

- a forward (`ssh ... -N -L [bind:]lport:host:rport dest`,
  `gcloud compute ssh vm ... -- ... -N -L ...`, or
  `gcloud compute start-iap-tunnel vm rport --local-host-port=localhost:lport`)
  listens on 127.0.0.1:lport and relays every connection to host:rport,
  until it is killed;
- a command (`ssh ... dest 'cmd'`, `gcloud compute ssh vm --command=cmd`)
  runs `cmd` with /bin/sh here.

Every invocation is appended to calls.log next to the stub.
"""
import os
import socket
import sys
import threading

SSH_OPTS_WITH_ARG = set("bcDEeFIiJLlmOoPpQRSWw")


def log(argv):
    path = os.path.join(os.path.dirname(os.path.abspath(sys.argv[0])), "calls.log")
    with open(path, "a") as f:
        f.write(" ".join(argv) + "\n")


def parse_ssh(args):
    forward = None
    no_command = False
    rest = []
    i = 0
    while i < len(args):
        a = args[i]
        if a == "--":
            rest = args[i + 1:]
            break
        if a.startswith("-") and len(a) > 1:
            flag = a[1]
            if flag in SSH_OPTS_WITH_ARG:
                val = a[2:] if len(a) > 2 else args[i + 1]
                if len(a) == 2:
                    i += 1
                if flag == "L":
                    forward = val
            elif "N" in a[1:]:
                no_command = True
            i += 1
            continue
        rest = args[i:]
        break
    dest = rest[0] if rest else None
    command = " ".join(rest[1:])
    return forward, no_command, dest, command


def relay(src, dst):
    try:
        while True:
            b = src.recv(65536)
            if not b:
                break
            dst.sendall(b)
    except OSError:
        pass
    finally:
        for s in (src, dst):
            try:
                s.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass


def serve_forward(lport, host, rport):
    host = host.strip("[]")
    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    try:
        srv.bind(("127.0.0.1", int(lport)))
    except OSError as e:
        # ExitOnForwardFailure=yes
        sys.stderr.write("bind [127.0.0.1]:%s: %s\n" % (lport, e))
        sys.exit(255)
    srv.listen(64)
    while True:
        c, _ = srv.accept()
        try:
            up = socket.create_connection((host, int(rport)), timeout=5)
            up.settimeout(None)
        except OSError as e:
            sys.stderr.write("channel open failed: %s\n" % e)
            c.close()
            continue
        threading.Thread(target=relay, args=(c, up), daemon=True).start()
        threading.Thread(target=relay, args=(up, c), daemon=True).start()


def split_forward(spec):
    # [bind:]lport:host:rport, host possibly [v6]
    if spec.count(":") >= 3 and not spec.startswith("["):
        _, spec = spec.split(":", 1)
    lport, rest = spec.split(":", 1)
    host, rport = rest.rsplit(":", 1)
    return lport, host, rport


def run_command(command):
    if not command:
        sys.stderr.write("stub: nothing to run\n")
        sys.exit(2)
    os.execv("/bin/sh", ["/bin/sh", "-c", command])


def main():
    name = os.path.basename(sys.argv[0])
    args = sys.argv[1:]
    log([name] + args)
    if name == "gcloud":
        if args[:2] == ["compute", "start-iap-tunnel"]:
            rport = args[3]
            lport = None
            for a in args[4:]:
                if a.startswith("--local-host-port="):
                    lport = a.split(":")[-1]
            serve_forward(lport, "127.0.0.1", rport)
            return
        if args[:2] == ["compute", "ssh"]:
            command = None
            ssh_args = []
            if "--" in args:
                cut = args.index("--")
                ssh_args = args[cut + 1:]
                args = args[:cut]
            for a in args:
                if a.startswith("--command="):
                    command = a[len("--command="):]
            forward, _, _, _ = parse_ssh(ssh_args + ["vm"])
            if forward:
                serve_forward(*split_forward(forward))
                return
            run_command(command)
            return
        sys.stderr.write("stub gcloud: unsupported %r\n" % args)
        sys.exit(2)
    forward, no_command, dest, command = parse_ssh(args)
    if dest is None:
        sys.stderr.write("stub ssh: no destination\n")
        sys.exit(255)
    if forward and no_command:
        serve_forward(*split_forward(forward))
        return
    run_command(command)


if __name__ == "__main__":
    main()
