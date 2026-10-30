#!/usr/bin/env python3
"""Private fixture provisioning and token-local native observations over SSH."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import secrets
import signal
import shutil
import stat
import subprocess
import sys
import tempfile

_native_spec = importlib.util.spec_from_file_location('native_upgrade', Path(__file__).with_name('native_upgrade.py'))
native_upgrade = importlib.util.module_from_spec(_native_spec)
_native_spec.loader.exec_module(native_upgrade)

ROOT = next((parent for parent in Path(__file__).resolve().parents if (parent / 'scripts/wow-test').is_file()), None)
if ROOT is None:
    raise RuntimeError('WoW project root unavailable')
TRANSPORT = ROOT / 'work/core1-native-earned-upgrade-delivery-20261021/activation/soap_transport.py'
REMOTE = r'''
import fcntl, hashlib, json, os, pathlib, re, secrets, stat, subprocess, sys, tempfile, time, urllib.request
mode, value = sys.argv[1:3]
private = pathlib.Path('/srv/wow/work/rapid-game-harness/private')
manifest = private / 'fixture.json'
pid = subprocess.check_output(['systemctl','show','azerothcore-worldserver.service','-p','MainPID','--value'],text=True).strip()
if not pid.isdigit() or pid == '0': raise RuntimeError('World is not running')
release = pathlib.Path(os.readlink('/proc/'+pid+'/exe')).parent.parent
values = {}
for line in (release/'etc/modules/gamestate_api.conf').read_text().splitlines():
 if line.strip().startswith('GameStateAPI.') and '=' in line:
  k,v=line.split('=',1); values[k.strip()]=v.strip().strip('"')
if values.get('GameStateAPI.Enable') != '1' or values.get('GameStateAPI.Host') != '127.0.0.1': raise RuntimeError('Loopback API unavailable')
def api(path):
 req=urllib.request.Request('http://127.0.0.1:'+values['GameStateAPI.Port']+path,headers={'Authorization':'Bearer '+values['GameStateAPI.AuthToken']})
 with urllib.request.urlopen(req,timeout=3) as response: return json.load(response)
def account(username):
 if not re.fullmatch(r'RH[A-F0-9]{12}',username): raise RuntimeError('Invalid fixture account')
 query="SELECT a.id,COALESCE(MAX(aa.gmlevel),0) FROM cpi225_auth_v7.account a LEFT JOIN cpi225_auth_v7.account_access aa ON aa.id=a.id WHERE a.username='"+username+"' GROUP BY a.id"
 result=subprocess.run(['podman','exec','azerothcore-mysql-cpi225-v7','sh','-c','MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot --batch --skip-column-names -e "$1"','fixture-read-only',query],capture_output=True,text=True,timeout=5)
 if result.returncode: raise RuntimeError('Fixture identity read failed')
 row=result.stdout.strip().split()
 if not row: return None
 if len(row)!=2 or int(row[0])<=0 or int(row[1])!=0: raise RuntimeError('Fixture privilege mismatch')
 return int(row[0])
def save_fixture(fixture):
 fd,temporary=tempfile.mkstemp(prefix='.fixture-',dir=private)
 try:
  with os.fdopen(fd,'w') as output: json.dump(fixture,output); output.flush(); os.fsync(output.fileno())
  os.replace(temporary,manifest)
  directory=os.open(private,os.O_RDONLY)
  try: os.fsync(directory)
  finally: os.close(directory)
 finally:
  pathlib.Path(temporary).unlink(missing_ok=True)
def private_open(path,flags):
 fd=os.open(path,flags|os.O_NOFOLLOW|os.O_NONBLOCK,0o600)
 try:
  info=os.fstat(fd)
  if not stat.S_ISREG(info.st_mode) or info.st_uid!=os.geteuid() or info.st_mode & 0o077: raise RuntimeError('Unsafe private fixture object')
  return fd
 except BaseException:
  os.close(fd)
  raise
def load_fixture():
 with os.fdopen(private_open(manifest,os.O_RDONLY),'rb') as source:
  if os.fstat(source.fileno()).st_size>4096: raise RuntimeError('Fixture manifest too large')
  raw=source.read(4097)
  if len(raw)>4096: raise RuntimeError('Fixture manifest too large')
  fixture=json.loads(raw)
 if not isinstance(fixture,dict): raise RuntimeError('Invalid fixture manifest')
 for key in ('account_id','character_guid'):
  if key in fixture and (type(fixture[key]) is not int or fixture[key]<=0): raise RuntimeError('Invalid fixture identity')
 if 'character_guid' in fixture and 'account_id' not in fixture: raise RuntimeError('Bound fixture account identity missing')
 return fixture
def character_identity(fixture):
 name=fixture['character']
 if not re.fullmatch(r'Rh[a-z]{10}',name): raise RuntimeError('Invalid owned character')
 query="SELECT guid,account,name FROM cpi225_characters_v7.characters WHERE name='"+name+"'"
 result=subprocess.run(['podman','exec','azerothcore-mysql-cpi225-v7','sh','-c','MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot --batch --skip-column-names -e "$1"','fixture-character-read-only',query],capture_output=True,text=True,timeout=5)
 if result.returncode: raise RuntimeError('Fixture character read failed')
 rows=result.stdout.strip().splitlines()
 if not rows: return None
 if len(rows)!=1: raise RuntimeError('Fixture character ambiguous')
 row=rows[0].split('\t')
 if len(row)!=3 or int(row[0])<=0 or int(row[1])!=fixture['account_id'] or row[2]!=name: raise RuntimeError('Fixture character identity changed')
 return int(row[0])
def world_identity():
 p=subprocess.check_output(['systemctl','show','azerothcore-worldserver.service','-p','MainPID','--value'],text=True).strip()
 i=subprocess.check_output(['systemctl','show','azerothcore-worldserver.service','-p','InvocationID','--value'],text=True).strip()
 if not p.isdigit() or p=='0' or not re.fullmatch(r'[a-f0-9]{32}',i):raise RuntimeError('World invocation unavailable')
 rows=pathlib.Path('/proc/'+p+'/status').read_text().splitlines()
 uid=next((line.split()[2] for line in rows if line.startswith('Uid:') and len(line.split())>=5),None)
 if uid is None or not uid.isdigit():raise RuntimeError('World owner unavailable')
 return {'pid':int(p),'invocation_id':i,'uid':int(uid),'exe':os.readlink('/proc/'+p+'/exe')}
def bind_character(fixture):
 guid=character_identity(fixture)
 if 'character_guid' in fixture:
  if guid!=fixture['character_guid']: raise RuntimeError('Bound fixture character missing or changed')
 elif guid is not None:
  fixture['character_guid']=guid
  fixture.pop('character_create_pending',None)
  save_fixture(fixture)
 elif fixture.get('character_create_pending') is not True:
  raise RuntimeError('Existing fixture character missing')
 return guid
# Local runner locks do not serialize separate checkouts or direct remote callers.
lock=None
if mode == 'prepare':
 # Include the nearest existing ancestor so retries durably publish a prior failed mkdir.
 directories=[private]
 while not directories[-1].exists(): directories.append(directories[-1].parent)
 for directory in reversed(directories):
  directory.mkdir(exist_ok=True,mode=0o700)
  parent=os.open(directory.parent,os.O_RDONLY|os.O_DIRECTORY)
  try: os.fsync(parent)
  finally: os.close(parent)
if private.exists():
 if private.is_symlink() or not private.is_dir() or private.stat().st_uid!=os.geteuid() or private.stat().st_mode & 0o077: raise RuntimeError('Unsafe private directory')
 lock=private_open(private/'.fixture.lock',os.O_RDWR|os.O_CREAT)
 deadline=time.monotonic()+5
 while True:
  try:
   fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
   break
  except BlockingIOError:
   if time.monotonic()>=deadline:
    os.close(lock)
    raise RuntimeError('Fixture operation busy')
   time.sleep(0.05)
elif mode != 'status': raise RuntimeError('Private fixture unavailable')
try:
 if mode == 'status':
  health=api('/api/health'); server=api('/api/server')
  fixture=load_fixture() if os.path.lexists(manifest) else {}
  owned=int(fixture.get('account_id',0))
  query='SELECT * FROM cpi225_characters_v7.characters WHERE account<>'+str(owned)+' ORDER BY guid'
  result=subprocess.run(['podman','exec','azerothcore-mysql-cpi225-v7','sh','-c','MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot --batch --skip-column-names -e "$1"','existing-character-read-only',query],capture_output=True,timeout=5)
  if result.returncode: raise RuntimeError('Existing-character snapshot failed')
  configs={name:hashlib.sha256((release/name).read_bytes()).hexdigest() for name in ('etc/worldserver.conf','etc/modules/gamestate_api.conf')}
  print(json.dumps({'pid':int(pid),'status':health['status'],'characters_in_world':server['characters_in_world'],'fixture_exists':manifest.exists(),'existing_character_rows_sha256':hashlib.sha256(result.stdout).hexdigest(),'protected_config_sha256':configs}))
 elif mode == 'prepare':
  if os.path.lexists(manifest):
   fixture=load_fixture()
  else:
   character='Rh'+''.join(secrets.choice('abcdefghijklmnopqrstuvwxyz') for _ in range(10))
   while re.search(r'(.)\1\1',character): character='Rh'+''.join(secrets.choice('abcdefghijklmnopqrstuvwxyz') for _ in range(10))
   fixture={'username':'RH'+secrets.token_hex(6).upper(),'password':secrets.token_hex(8).upper(),'character':character,'auth_addr':value+':3724','character_create_pending':True}
   if account(fixture['username']) is not None: raise RuntimeError('Fixture account collision')
   # Publish the whole retry identity durably before any native mutation.
   save_fixture(fixture)
  if not re.fullmatch(r'RH[A-F0-9]{12}',fixture['username']) or not re.fullmatch(r'Rh[a-z]{10}',fixture['character']): raise RuntimeError('Invalid owned fixture')
  identity=account(fixture['username'])
  if 'account_id' in fixture and identity!=fixture['account_id']: raise RuntimeError('Fixture account identity changed')
  if len(fixture['password'])>16: raise RuntimeError('Fixture password exceeds native limit')
  if identity is None:
   save_fixture(fixture)
   result=soap_command('account create '+fixture['username']+' '+fixture['password'],read_credentials('/etc/azerothcore/cpi188-cast.env'))
   identity=account(fixture['username'])
   if not result['ok'] or identity is None: raise RuntimeError('Native account provisioning rejected')
  fixture['account_id']=identity
  save_fixture(fixture)
  bind_character(fixture)
  print(json.dumps({key:fixture[key] for key in ('auth_addr','username','password','character')}))
 elif mode == 'verify':
  fixture=load_fixture()
  if any(expected[key]!=fixture[key] for key in ('auth_addr','username','password','character')): raise RuntimeError('Config is not the recorded fixture')
  if account(fixture['username'])!=fixture['account_id']: raise RuntimeError('Fixture account changed')
  bind_character(fixture)
  print(json.dumps({'verified':True}))
 elif mode == 'observe':
  if not re.fullmatch(r'Rh[A-Za-z]{2,10}',value): raise RuntimeError('Not a harness character')
  fixture=load_fixture()
  if fixture['character']!=value: raise RuntimeError('Unowned fixture')
  if account(fixture['username'])!=fixture['account_id']: raise RuntimeError('Fixture account changed')
  guid=bind_character(fixture)
  if guid is None: raise RuntimeError('Fixture character not created')
  data=api('/api/player/'+value)
  if data['name']!=value or data['guid']!=guid or data['security_level']!=0 or data['account_id']!=fixture['account_id']: raise RuntimeError('Fixture identity/privilege mismatch')
  print(json.dumps({'name':data['name'],'guid':data['guid'],'map':data['map_id'],'x':data['position']['x'],'y':data['position']['y'],'z':data['position']['z'],'level':data['level'],'health':data['health']['current'],'mana':data['power']['current'],'online':data['online']}))
 elif mode == 'native':
  before=world_identity()
  soap=soap_command(native_command,read_credentials('/etc/azerothcore/cpi188-cast.env'))
  after=world_identity()
  if before!=after:raise RuntimeError('World invocation changed during native call')
  print(json.dumps({'process':before,'soap':soap}))
 else: raise RuntimeError('Unknown operation')
finally:
 if lock is not None: os.close(lock)
'''

STREAM = r'''
import hashlib,json,os,pathlib,re,stat,subprocess,sys
_data=json.loads(TRANSFER)
_path=pathlib.PurePosixPath(_data['path'])
_root=_path.parent
_parts=_root.parts
if _parts not in (('/','srv','wow','work','core1-native-earned-upgrade-delivery-20261021','captures'),
                  ('/','srv','wow','work','core3-native-earned-upgrade-harness-20261022','status')):raise RuntimeError('unapproved private root')
def _world():
 _pid=subprocess.check_output(['systemctl','show','azerothcore-worldserver.service','-p','MainPID','--value'],text=True).strip()
 _inv=subprocess.check_output(['systemctl','show','azerothcore-worldserver.service','-p','InvocationID','--value'],text=True).strip()
 if not _pid.isdigit() or _pid=='0' or not re.fullmatch(r'[a-f0-9]{32}',_inv):raise RuntimeError('World invocation unavailable')
 _uids=pathlib.Path('/proc/'+_pid+'/status').read_text().splitlines()
 _uid=next((line.split()[2] for line in _uids if line.startswith('Uid:') and len(line.split())>=5),None)
 if _uid is None or not _uid.isdigit():raise RuntimeError('World owner unavailable')
 return {'pid':int(_pid),'invocation_id':_inv,'uid':int(_uid),'exe':os.readlink('/proc/'+_pid+'/exe')}
if _world()!=_data['process']:raise RuntimeError('World invocation changed before stream')
_fd=os.open('/',os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
for _part in _parts[1:]:
 _next=os.open(_part,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW,dir_fd=_fd);os.close(_fd);_fd=_next
_dir=os.fstat(_fd)
if not stat.S_ISDIR(_dir.st_mode) or _dir.st_uid!=_data['process']['uid'] or stat.S_IMODE(_dir.st_mode)!=0o700:raise RuntimeError('private source directory owner/mode')
_name=_path.name
if not ((_parts[-1]=='captures' and re.fullmatch(r'receipt-[0-9a-f]{32}\.json',_name)) or
        (_parts[-1]=='status' and re.fullmatch(r'status-[0-9a-f]{64}\.json',_name))):raise RuntimeError('unapproved direct-child name')
_src=os.open(_name,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK,dir_fd=_fd)
try:
 _before=os.fstat(_src);_entry=os.stat(_name,dir_fd=_fd,follow_symlinks=False)
 if not stat.S_ISREG(_before.st_mode) or _before.st_uid!=_data['process']['uid'] or stat.S_IMODE(_before.st_mode)!=0o600 or _before.st_nlink!=1 or _before.st_size!=_data['bytes'] or (_before.st_dev,_before.st_ino)!=(_entry.st_dev,_entry.st_ino):raise RuntimeError('private source file owner/mode/shape')
 _hash=hashlib.sha256();_left=_data['bytes'];_out=sys.stdout.buffer
 while _left:
  _block=os.read(_src,min(1048576,_left))
  if not _block:raise RuntimeError('private source early EOF')
  _hash.update(_block);_view=memoryview(_block)
  while _view:
   _written=_out.write(_view)
   if not _written:raise RuntimeError('stream output short write')
   _view=_view[_written:]
  _left-=len(_block)
 if os.read(_src,1):raise RuntimeError('private source trailing bytes')
 _after=os.fstat(_src);_entry_after=os.stat(_name,dir_fd=_fd,follow_symlinks=False)
 _stable=lambda _s:(_s.st_dev,_s.st_ino,_s.st_size,_s.st_mtime_ns,_s.st_ctime_ns)
 if _stable(_before)!=_stable(_after) or _stable(_before)!=_stable(_entry_after) or _hash.hexdigest()!=_data['sha256']:raise RuntimeError('private source changed or digest mismatch')
finally:
 os.close(_src);os.close(_fd)
_out.flush()
if _world()!=_data['process']:raise RuntimeError('World invocation changed during stream')
'''

STREAM_CHUNK = 1024 * 1024
STREAM_SSH = ['ssh', '-T', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=5', 'server-01', 'sudo', '-n', 'python3', '-']


def _stream_program(descriptor, process):
    payload = {
        'path': descriptor['path'], 'bytes': descriptor['bytes'], 'sha256': descriptor['sha256'],
        'process': process,
    }
    encoded = json.dumps(payload, separators=(',', ':'), sort_keys=True)
    return STREAM.replace('TRANSFER', repr(encoded))


def _private_receipts():
    root = ROOT / 'work/core3-native-earned-upgrade-harness-20261022/receipts'
    if root.is_symlink():
        raise ValueError('Private receipt directory must not be a symlink')
    root.mkdir(parents=True, exist_ok=True, mode=0o700)
    info = root.stat(follow_symlinks=False)
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o700:
        raise ValueError('Private receipt directory must be owned mode0700')
    return root


def _process(value):
    if type(value) is not dict or set(value) != {'pid', 'invocation_id', 'uid', 'exe'}:
        raise ValueError('Invalid native process binding')
    if type(value['pid']) is not int or value['pid'] <= 0 or type(value['uid']) is not int or value['uid'] < 0:
        raise ValueError('Invalid native process identity')
    if not isinstance(value['invocation_id'], str) or re.fullmatch(r'[a-f0-9]{32}', value['invocation_id']) is None:
        raise ValueError('Invalid native systemd invocation')
    if not isinstance(value['exe'], str) or not value['exe'].startswith('/'):
        raise ValueError('Invalid native executable identity')
    return value


def stream_file(descriptor, process):
    """Copy an exact private server file using constant-size chunks and no replacement."""
    process = _process(process)
    if type(descriptor) is not dict or type(descriptor.get('bytes')) is not int or descriptor['bytes'] <= 0:
        raise ValueError('Invalid native file descriptor')
    if not isinstance(descriptor.get('sha256'), str) or re.fullmatch(r'[a-f0-9]{64}', descriptor['sha256']) is None:
        raise ValueError('Invalid native file digest')
    path = descriptor.get('path')
    if not isinstance(path, str):
        raise ValueError('Invalid native file path')
    is_capture = path.startswith('/srv/wow/work/core1-native-earned-upgrade-delivery-20261021/captures/')
    if is_capture:
        if set(descriptor) != {'path', 'bytes', 'sha256', 'generation', 'ready'}:
            raise ValueError('Incomplete native capture descriptor')
        checked = native_upgrade.capture_descriptor(descriptor)
    else:
        checked = native_upgrade.status_descriptor(descriptor)
    descriptor = {**checked, 'path': checked['path']}
    target = _private_receipts() / Path(descriptor['path']).name
    if target.exists() or target.is_symlink():
        info = target.stat(follow_symlinks=False)
        if (is_capture or target.is_symlink() or not stat.S_ISREG(info.st_mode) or
                info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o600 or info.st_nlink != 1 or
                info.st_size != descriptor['bytes'] or _file_digest(target) != descriptor['sha256']):
            raise FileExistsError('Native immutable transfer target conflicts')
        return {'path': str(target), 'bytes': info.st_size, 'sha256': descriptor['sha256'],
                **({'generation': descriptor['generation']} if is_capture else {})}
    if shutil.disk_usage(target.parent).free < descriptor['bytes']:
        raise OSError('Insufficient local space for exact native receipt size')
    fd, temporary = tempfile.mkstemp(prefix='.native-transfer-', dir=target.parent)
    proc = None
    try:
        os.fchmod(fd, 0o600)
        source = _stream_program(descriptor, process).encode()
        proc = subprocess.Popen(STREAM_SSH, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
        proc.stdin.write(source)
        proc.stdin.close()
        digest = hashlib.sha256()
        total = 0
        with os.fdopen(fd, 'wb', closefd=True) as output:
            fd = -1
            while True:
                block = os.read(proc.stdout.fileno(), STREAM_CHUNK)
                if not block:
                    break
                total += len(block)
                if total > descriptor['bytes']:
                    raise ValueError('Native file stream exceeded its descriptor size')
                digest.update(block)
                output.write(block)
            # The Go caller's CommandContext owns cancellation/deadlines.  Do
            # not impose a second, fixed acquisition deadline here.
            code = proc.wait()
            if code != 0:
                raise RuntimeError('Native private file stream failed')
            if total != descriptor['bytes'] or digest.hexdigest() != descriptor['sha256']:
                raise ValueError('Native private file length or digest mismatch')
            output.flush()
            os.fsync(output.fileno())
        os.link(temporary, target, follow_symlinks=False)
        os.unlink(temporary)
        directory = os.open(target.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
        info = target.stat(follow_symlinks=False)
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o600 or info.st_nlink != 1 or info.st_size != descriptor['bytes']:
            raise RuntimeError('Local native receipt publication changed')
        return {'path': str(target), 'bytes': total, 'sha256': digest.hexdigest(),
                **({'generation': descriptor['generation']} if is_capture else {})}
    except BaseException:
        if proc is not None and proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=2)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
        if fd >= 0:
            os.close(fd)
        Path(temporary).unlink(missing_ok=True)
        raise


def validate_name(name):
    if not re.fullmatch(r'Rh[A-Za-z]{2,10}', name):
        raise ValueError('Expected a dedicated Rh character name')


def _file_digest(path):
    digest = hashlib.sha256()
    with open(path, 'rb') as source:
        for block in iter(lambda: source.read(STREAM_CHUNK), b''):
            digest.update(block)
    return digest.hexdigest()


def _unique_object(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise ValueError('duplicate key in native status JSON')
        value[key] = item
    return value


def _reject_constant(value):
    raise ValueError('nonfinite native status JSON value')


def private_destination(value):
    path = Path(value)
    private = ROOT / 'work/rapid-game-harness-20261012/private'
    if not path.is_absolute() or path.parent != private or path.is_symlink() or private.is_symlink():
        raise ValueError('Config must be a direct file in the harness private directory')
    if private.exists() and (private.stat().st_mode & 0o777 != 0o700 or private.stat().st_uid != os.geteuid()):
        raise ValueError('Private directory must have mode0700')
    if path.exists() and (not path.is_file() or path.stat().st_mode & 0o777 != 0o600 or path.stat().st_uid != os.geteuid()):
        raise ValueError('Private config must be a regular0600 file')
    return path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode', choices=('prepare', 'verify', 'observe', 'status', 'native'))
    parser.add_argument('value', nargs='?', default='')
    parser.add_argument('fingerprint', nargs='?', default='')
    parser.add_argument('--native-args', default='{}')
    args = parser.parse_args()
    if args.mode == 'observe':
        validate_name(args.value)
    elif args.mode == 'prepare' and not args.value:
        parser.error('prepare requires the private config destination')
    elif args.mode == 'verify' and not args.value:
        parser.error('verify requires the private config path')
    elif args.mode == 'native':
        if not args.value:
            parser.error('native requires one fixed operation name')
        values = json.loads(args.native_args)
        if type(values) is not dict:
            raise ValueError('Native operation arguments must be an object')
        if args.value == 'drain_capture':
            if values:
                raise ValueError('Native file operations do not accept caller paths')
            args.native_nonce = secrets.token_hex(16)
            values = {'nonce': args.native_nonce}
        args.native_values = values
        args.native_command = native_upgrade.command(args.value, values)
    if args.fingerprint and (args.mode != 'verify' or not re.fullmatch(r'[a-f0-9]{64}', args.fingerprint)):
        raise ValueError('Invalid private config fingerprint')
    destination = private_destination(args.value) if args.mode == 'prepare' else None
    temporary = None
    try:
        if destination:
            destination.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            fd, temporary = tempfile.mkstemp(prefix='.fixture-', dir=destination.parent)
            os.close(fd)
        execute(args, destination, temporary)
    finally:
        if temporary:
            Path(temporary).unlink(missing_ok=True)


def execute(args, destination, temporary):
    prefix = ''
    if args.mode == 'verify':
        # Reuse the same descriptor validation used by the Go caller; the private payload travels only on SSH stdin.
        from config import validate
        expected = validate(args.value)
        fingerprint = hashlib.sha256('\0'.join(expected[key] for key in ('auth_addr', 'username', 'password', 'character')).encode()).hexdigest()
        if args.fingerprint and args.fingerprint != fingerprint:
            raise ValueError('Private config changed after validation')
        prefix = 'expected = ' + repr(expected) + '\n'
    elif args.mode == 'native':
        prefix = 'native_command = ' + repr(args.native_command) + '\n'
    address = subprocess.check_output(['ssh', '-G', 'server-01'], stderr=subprocess.DEVNULL, text=True)
    host = next(line.split(' ', 1)[1] for line in address.splitlines() if line.startswith('hostname '))
    if not re.fullmatch(r'[A-Za-z0-9.-]+', host):
        raise ValueError('Invalid auth host')
    value = host if args.mode == 'prepare' else '-' if args.mode == 'verify' else args.value or '-'
    result = subprocess.run(['ssh', '-T', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=5', 'server-01',
                             'sudo', '-n', 'python3', '-', args.mode, value],
                            input=TRANSPORT.read_text() + '\n' + prefix + REMOTE, text=True, capture_output=True, timeout=15)
    if result.returncode:
        # Remote tracebacks/HTTP bodies can contain private command data; never relay them.
        raise RuntimeError('Remote ' + args.mode + ' failed (credentials and private diagnostics suppressed)')
    data = json.loads(result.stdout)
    if args.mode == 'prepare':
        with open(temporary, 'w') as output:
            json.dump(data, output)
            output.flush()
            os.fsync(output.fileno())
        private_destination(str(destination))
        os.replace(temporary, destination)
        print(json.dumps({'prepared': True, 'character': data['character'], 'config': str(destination)}))
    elif args.mode == 'native':
        if type(data) is not dict or set(data) != {'process', 'soap'}:
            raise ValueError('Incomplete native remote receipt')
        process = _process(data['process'])
        value = native_upgrade.result_value(data['soap'], native_upgrade.parse_frames)
        if args.value in {'export_status', 'drain_capture'}:
            if args.value == 'export_status':
                descriptor = native_upgrade.status_descriptor(value)
            else:
                descriptor = native_upgrade.capture_descriptor(value)
            copied = stream_file(descriptor, process)
            if args.value == 'export_status':
                with open(copied['path'], 'rb') as source:
                    payload = json.load(source, object_pairs_hook=_unique_object, parse_constant=_reject_constant)
                if type(payload) is not dict or set(payload) != {'schema', 'status'} or payload['schema'] != 'cpi388-native-witness-status-v1' or type(payload['status']) is not dict:
                    raise ValueError('Native status export schema mismatch')
                print(json.dumps({'status': payload['status'], 'descriptor': descriptor, 'transfer': copied}))
            else:
                print(json.dumps({'capture': copied, 'descriptor': descriptor}))
        else:
            print(json.dumps(value))
    else:
        print(json.dumps(data))


if __name__ == '__main__':
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(143))
    try:
        main()
    except (ValueError, RuntimeError, OSError, subprocess.SubprocessError, json.JSONDecodeError) as error:
        print(str(error) if isinstance(error, (ValueError, RuntimeError)) else 'Harness remote transport failed', file=sys.stderr)
        sys.exit(1)
