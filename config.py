#!/usr/bin/env python3
"""Validate a fixture locally; errors never include file contents or credentials."""
import json
import os
import re
import stat
import sys
import tempfile
from pathlib import Path


def validate(path):
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(fd, 'rb') as source:
            info = os.fstat(source.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077 or info.st_uid != os.getuid():
                raise ValueError('Fixture must be an owner-only regular file owned by this user')
            if info.st_size > 4096:
                raise ValueError('Fixture exceeds the 4096-byte limit')
            raw = source.read(4097)
            if len(raw) > 4096:
                raise ValueError('Fixture exceeds the 4096-byte limit')
            data = json.loads(raw)
    except (OSError, UnicodeError, json.JSONDecodeError):
        raise ValueError('Fixture is missing, unreadable, or malformed') from None
    if not isinstance(data, dict) or set(data) != {'auth_addr', 'username', 'password', 'character'}:
        raise ValueError('Fixture requires exactly auth_addr, username, password, character')
    if any(not isinstance(value, str) or not value for value in data.values()):
        raise ValueError('Fixture fields must be nonempty strings')
    if not re.fullmatch(r'RH[A-Z0-9]{6,14}', data['username']):
        raise ValueError('Expected a dedicated RH account')
    if not re.fullmatch(r'Rh[A-Za-z]{2,10}', data['character']):
        raise ValueError('Expected a dedicated Rh character')
    if len(data['password']) > 16 or any(ord(c) < 33 or ord(c) > 126 for c in data['password']):
        raise ValueError('Fixture password must be 1-16 printable non-whitespace ASCII characters')
    if not re.fullmatch(r'(?:[A-Za-z0-9.-]+|\[[0-9A-Fa-f:]+\]):[0-9]{1,5}', data['auth_addr']):
        raise ValueError('Expected auth host:port')
    if not 0 < int(data['auth_addr'].rsplit(':', 1)[1]) <= 65535:
        raise ValueError('Invalid auth port')
    return data


if __name__ == '__main__':
    try:
        if len(sys.argv) == 3 and sys.argv[1] == '--reserve':
            private = Path(sys.argv[2])
            private.mkdir(parents=True, exist_ok=True, mode=0o700)
            info = private.stat()
            if private.is_symlink() or info.st_mode & 0o077 or info.st_uid != os.getuid():
                raise ValueError('Unsafe private snapshot directory')
            fd, path = tempfile.mkstemp(prefix='.run-config-', suffix='.json', dir=private)
            os.close(fd)
            print(path)
            sys.exit(0)
        if len(sys.argv) == 4 and sys.argv[1] == '--snapshot':
            data = validate(sys.argv[2])
            fd = os.open(sys.argv[3], os.O_WRONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
            with os.fdopen(fd, 'w') as output:
                info = os.fstat(output.fileno())
                if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077 or info.st_uid != os.getuid():
                    raise ValueError('Unsafe private snapshot file')
                os.ftruncate(output.fileno(), 0)
                json.dump(data, output)
            sys.exit(0)
        emit_json = len(sys.argv) == 3 and sys.argv[1] == '--json'
        if len(sys.argv) != 2 and not emit_json:
            raise ValueError('WOW_TEST_CONFIG must name a private fixture file')
        data = validate(sys.argv[-1])
        if emit_json:
            json.dump(data, sys.stdout)
    except ValueError as error:
        print(error, file=sys.stderr)
        sys.exit(1)
