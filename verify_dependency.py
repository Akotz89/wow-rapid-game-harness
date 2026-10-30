#!/usr/bin/env python3
"""Compare actual dependency inputs with an independently patched pinned tree."""
import os
import io
from pathlib import Path
import stat
import subprocess
import sys


def git(dep, *args):
    return subprocess.check_output(['git', '--no-replace-objects', '-C', str(dep), *args])


def materialize(dep, destination, revision):
    records = []
    for record in git(dep, 'ls-tree', '-rz', '--full-tree', revision).split(b'\0'):
        if not record:
            continue
        metadata, name = record.split(b'\t', 1)
        mode, kind, oid = metadata.split()
        if kind != b'blob' or mode not in (b'100644', b'100755', b'120000'):
            raise ValueError('Unsupported pinned dependency entry')
        records.append((mode, oid, Path(os.fsdecode(name))))
    process = subprocess.run(['git', '--no-replace-objects', '-C', str(dep), 'cat-file', '--batch'],
                             input=b''.join(oid + b'\n' for _, oid, _ in records), capture_output=True, check=True)
    content = io.BytesIO(process.stdout)
    for mode, oid, name in records:
        header = content.readline().split()
        if len(header) != 3 or header[:2] != [oid, b'blob']:
            raise ValueError('Invalid pinned dependency object')
        data = content.read(int(header[2]))
        if len(data) != int(header[2]) or content.read(1) != b'\n':
            raise ValueError('Truncated pinned dependency object')
        path = destination / name
        path.parent.mkdir(parents=True, exist_ok=True)
        if mode == b'120000':
            path.symlink_to(os.fsdecode(data))
        else:
            path.write_bytes(data)
            path.chmod(0o755 if mode == b'100755' else 0o644)


def entries(root):
    result = {}
    for folder, dirs, files in os.walk(root, followlinks=False):
        if Path(folder) == root:
            dirs[:] = [name for name in dirs if name != '.git']
            files = [name for name in files if name != '.git']
        for name in dirs + files:
            path = Path(folder) / name
            if path.is_symlink() or not path.is_dir():
                result[path.relative_to(root)] = path
    return result


def verify(dep, expected, revision):
    actual, wanted = entries(dep), entries(expected)
    if actual.keys() != wanted.keys():
        raise ValueError('Unexpected missing or untracked dependency inputs (including ignored files)')
    patches = {Path('client/auth.go'), Path('e2e/e2eharness/session.go'), Path('client/world.go'), Path('client/session.go')}
    pending = []
    for name, path in actual.items():
        reference = wanted[name]
        info, target = path.lstat(), reference.lstat()
        if stat.S_IFMT(info.st_mode) != stat.S_IFMT(target.st_mode) or bool(info.st_mode & 0o111) != bool(target.st_mode & 0o111):
            raise ValueError('Unexpected dependency input type or executable mode')
        if path.is_symlink():
            if os.readlink(path) != os.readlink(reference):
                raise ValueError('Unexpected dependency symlink')
        elif not path.is_file():
            raise ValueError('Unexpected dependency input type')
        elif path.read_bytes() != reference.read_bytes():
            if name not in patches or path.read_bytes() != git(dep, 'show', revision + ':' + name.as_posix()):
                raise ValueError('Unexpected dependency content')
            pending.append(name)
    # Apply only approved changes, after every actual input passed inspection.
    for name in pending:
        os.replace(wanted[name], actual[name])


if __name__ == '__main__':
    try:
        if len(sys.argv) == 5 and sys.argv[1] == '--materialize':
            materialize(Path(sys.argv[2]), Path(sys.argv[3]), sys.argv[4])
        else:
            verify(Path(sys.argv[1]), Path(sys.argv[2]), sys.argv[3])
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        sys.exit(str(error))
