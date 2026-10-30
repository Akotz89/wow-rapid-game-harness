"""Source-only, fixed-call binding for the Core1 native gear witness."""
import json
from pathlib import PurePosixPath
import re


def _text(value, label):
    if not isinstance(value, str) or not value or len(value) > 4096:
        raise ValueError("invalid " + label)
    return value


def _digest(value, label):
    if not isinstance(value, str) or re.fullmatch(r"[0-9a-f]{64}", value) is None:
        raise ValueError("invalid " + label)
    return value


def _uint(value, label):
    if type(value) is not int or value <= 0:
        raise ValueError("invalid " + label)
    return value


def _slot(value):
    if not isinstance(value, str) or re.fullmatch(r"quest:[1-9][0-9]*:(?:choice|fixed):[0-9]+", value) is None:
        raise ValueError("invalid exact native slot key")
    return value


def _path(value, label):
    value = _text(value, label)
    if value.startswith("work/core1-native-earned-upgrade-delivery-20261021/"):
        value = "/srv/wow/" + value
    path = PurePosixPath(value)
    allowed = ("/srv/wow/work/core1-native-earned-upgrade-delivery-20261021/",
               "/srv/wow/work/core3-native-earned-upgrade-harness-20261022/")
    if not path.is_absolute() or ".." in path.parts or not value.startswith(allowed):
        raise ValueError("path outside the accepted private work roots")
    return value


def _nonce(value):
    if not isinstance(value, str) or re.fullmatch(r"[0-9a-f]{32}", value) is None:
        raise ValueError("invalid transfer nonce")
    return value


def _capture_path(value):
    value = _text(value, "capture path")
    path = PurePosixPath(value)
    if (not path.is_absolute() or ".." in path.parts or
            path.parent != PurePosixPath("/srv/wow/work/core1-native-earned-upgrade-delivery-20261021/captures") or
            re.fullmatch(r"receipt-[0-9a-f]{32}\.json", path.name) is None):
        raise ValueError("capture path is not an exact Core1 receipt child")
    return value


def _capture_descriptor(value):
    fields = {"path", "bytes", "sha256", "generation", "ready"}
    if type(value) is not dict or set(value) != fields:
        raise ValueError("invalid capture descriptor")
    size = value["bytes"]
    if type(size) is not int or size <= 0:
        raise ValueError("invalid capture byte count")
    if value["ready"] is not False:
        raise ValueError("capture descriptor is not an unready original")
    return {
        "path": _capture_path(value["path"]),
        "bytes": size,
        "sha256": _digest(value["sha256"], "descriptor digest"),
        "generation": _text(value["generation"], "capture generation"),
        "ready": False,
    }


def _status_descriptor(value):
    fields = {"path", "bytes", "sha256"}
    if type(value) is not dict or set(value) != fields:
        raise ValueError("invalid status descriptor")
    path = _text(value["path"], "status path")
    parsed = PurePosixPath(path)
    if (not parsed.is_absolute() or ".." in parsed.parts or
            parsed.parent != PurePosixPath("/srv/wow/work/core3-native-earned-upgrade-harness-20261022/status") or
            re.fullmatch(r"status-[0-9a-f]{64}\.json", parsed.name) is None):
        raise ValueError("status path is not an exact content-addressed child")
    size = _uint(value["bytes"], "status byte count")
    digest = _digest(value["sha256"], "status digest")
    if parsed.stem.removeprefix("status-") != digest:
        raise ValueError("status filename/digest mismatch")
    return {"path": path, "bytes": size, "sha256": digest}


def capture_descriptor(value):
    return _capture_descriptor(value)


def status_descriptor(value):
    return _status_descriptor(value)


CAPTURE_ROOT = "/srv/wow/work/core1-native-earned-upgrade-delivery-20261021/captures"
ACK_ROOT = "/srv/wow/work/core1-native-earned-upgrade-delivery-20261021/acks"
STATUS_ROOT = "/srv/wow/work/core3-native-earned-upgrade-harness-20261022/status"


def _ack_path(descriptor):
    receipt = _capture_descriptor(descriptor)
    receipt_id = PurePosixPath(receipt["path"]).stem.removeprefix("receipt-")
    return ACK_ROOT + "/ack-" + receipt_id + ".json"


def _status_export_code():
    """Fixed private immutable publication of the existing module-global status."""
    return "\n".join((
        "import hashlib,json,os,stat",
        "_value=witness_status()",
        "_raw=json.dumps({'schema':'cpi388-native-witness-status-v1','status':_value},sort_keys=True,separators=(',',':'),ensure_ascii=False,allow_nan=False).encode('utf-8')",
        "_digest=hashlib.sha256(_raw).hexdigest()",
        "_base=os.open('/',os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)",
        "for _part in ('srv','wow','work'):",
        " _next=os.open(_part,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW,dir_fd=_base);os.close(_base);_base=_next",
        "def _owned_dir(_parent,_name,_create=False):",
        " try:_fd=os.open(_name,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW,dir_fd=_parent)",
        " except FileNotFoundError:",
        "  if not _create:raise",
        "  os.mkdir(_name,0o700,dir_fd=_parent);_fd=os.open(_name,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW,dir_fd=_parent)",
        " _st=os.fstat(_fd)",
        " if not stat.S_ISDIR(_st.st_mode) or _st.st_uid!=os.geteuid() or stat.S_IMODE(_st.st_mode)!=0o700:raise RuntimeError('unsafe native status directory')",
        " return _fd",
        "_task=_owned_dir(_base,'core3-native-earned-upgrade-harness-20261022',True)",
        "_dir=_owned_dir(_task,'status',True)",
        "_name='status-'+_digest+'.json'",
        "try:_existing=os.open(_name,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK,dir_fd=_dir)",
        "except FileNotFoundError:_existing=None",
        "if _existing is None:",
        " _tmp='.status-'+_digest+'.tmp-'+str(os.getpid())",
        " _fd=os.open(_tmp,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600,dir_fd=_dir)",
        " try:",
        "  os.fchmod(_fd,0o600);_view=memoryview(_raw)",
        "  while _view:",
        "   _n=os.write(_fd,_view)",
        "   if _n<=0:raise OSError('short native status write')",
        "   _view=_view[_n:]",
        "  os.fsync(_fd);_st=os.fstat(_fd)",
        "  if not stat.S_ISREG(_st.st_mode) or _st.st_uid!=os.geteuid() or stat.S_IMODE(_st.st_mode)!=0o600 or _st.st_nlink!=1 or _st.st_size!=len(_raw):raise RuntimeError('native status publication shape')",
        " finally:os.close(_fd)",
        " try:os.link(_tmp,_name,src_dir_fd=_dir,dst_dir_fd=_dir,follow_symlinks=False)",
        " except FileExistsError:pass",
        " finally:os.unlink(_tmp,dir_fd=_dir)",
        " os.fsync(_dir)",
        "_fd=os.open(_name,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK,dir_fd=_dir)",
        "try:",
        " _before=os.fstat(_fd);_entry=os.stat(_name,dir_fd=_dir,follow_symlinks=False)",
        " if not stat.S_ISREG(_before.st_mode) or _before.st_uid!=os.geteuid() or stat.S_IMODE(_before.st_mode)!=0o600 or _before.st_nlink!=1 or _before.st_size!=len(_raw) or (_before.st_dev,_before.st_ino)!=(_entry.st_dev,_entry.st_ino):raise RuntimeError('native status file owner/mode/shape')",
        " _read=bytearray()",
        " while True:",
        "  _block=os.read(_fd,1048576)",
        "  if not _block:break",
        "  _read.extend(_block)",
        " _after=os.fstat(_fd);_entry_after=os.stat(_name,dir_fd=_dir,follow_symlinks=False)",
        " _stable=lambda _s:(_s.st_dev,_s.st_ino,_s.st_size,_s.st_mtime_ns,_s.st_ctime_ns)",
        " if _stable(_before)!=_stable(_after) or _stable(_before)!=_stable(_entry_after) or bytes(_read)!=_raw or hashlib.sha256(_read).hexdigest()!=_digest:raise RuntimeError('native status changed or digest mismatch')",
        "finally:os.close(_fd)",
        "__import__('capture_native_acquisition').NativeCaptureFrames.emit({'path':'" + STATUS_ROOT + "/'+_name,'bytes':len(_raw),'sha256':_digest})",
        "os.close(_dir);os.close(_task);os.close(_base)",
    ))


def parse_frames(response):
    lines = response.splitlines()
    if not lines or lines[0] != "Output:" or any("lines truncated" in line for line in lines):
        raise ValueError("invalid NativeCaptureFrames output")
    try:
        value = json.loads("".join(lines[1:]))
    except json.JSONDecodeError as error:
        raise ValueError("incomplete NativeCaptureFrames JSON") from error
    if type(value) is not dict:
        raise ValueError("NativeCaptureFrames output must be an object")
    return value


def _call(operation, args):
    """Return only literal calls to the accepted module-global wrappers."""
    if type(args) is not dict:
        raise ValueError("operation arguments must be an object")
    if operation == "export_status" and not args:
        return "exec(" + repr(_status_export_code()) + ")"
    if operation == "queue_gear_observation" and set(args) == {"slot_key"}:
        return "queue_native_gear_observation(" + repr(_slot(args["slot_key"])) + ")"
    if operation == "stage_gear" and set(args) == {"path", "sha256"}:
        return "stage_accepted_gear(" + repr(_path(args["path"], "gear path")) + "," + repr(_digest(args["sha256"], "gear digest")) + ")"
    if operation == "arm_capture" and set(args) == {"identity_key", "binding", "slot_key", "snapshot_id", "exporter_revision"}:
        key = _digest(args["identity_key"], "identity key")
        binding = args["binding"]
        if type(binding) is not list or len(binding) != 2:
            raise ValueError("invalid actor binding")
        account, guid = (_uint(binding[0], "account id"), _uint(binding[1], "character guid"))
        return "arm_native_quest_capture(" + ",".join((repr(key), repr([account, guid]),
            repr(_slot(args["slot_key"])), repr(_text(args["snapshot_id"], "snapshot id")),
            repr(_digest(args["exporter_revision"], "exporter revision")))) + ")"
    if operation == "set_mode" and set(args) == {"mode"}:
        mode = args["mode"]
        if mode not in {"observe", "gear", "disarm", "relog"}:
            raise ValueError("unsupported witness mode")
        return "witness_command(" + repr(mode) + ")"
    if operation == "request_session" and not args:
        return "request_witness_session()"
    if operation == "drain_capture" and set(args) == {"nonce"}:
        path = "/srv/wow/work/core1-native-earned-upgrade-delivery-20261021/captures/receipt-" + _nonce(args["nonce"]) + ".json"
        return "drain_acquisition_capture(" + repr(_capture_path(path)) + ")"
    if operation == "release_capture" and not args:
        return "release_acquisition_capture()"
    if operation == "recover_capture" and set(args) == {"descriptor"}:
        descriptor = _capture_descriptor(args["descriptor"])
        return "recover_native_quest_capture(" + repr(descriptor) + "," + repr(CAPTURE_ROOT) + ")"
    if operation == "prepare_ack" and set(args) == {"descriptor"}:
        descriptor = _capture_descriptor(args["descriptor"])
        return "prepare_native_custody_ack(" + ",".join((repr(descriptor), repr(CAPTURE_ROOT), repr(_ack_path(descriptor)))) + ")"
    if operation == "recover_ack" and set(args) == {"descriptor"}:
        descriptor = _capture_descriptor(args["descriptor"])
        return "recover_native_custody_ack(" + repr(_ack_path(descriptor)) + ")"
    if operation == "settle_ack" and not args:
        return "settle_native_custody_ack()"
    raise ValueError("unknown operation or wrong argument set")


def command(operation, args):
    """Build a bounded command; user text can enter only as validated wrapper arguments."""
    call = _call(operation, args)
    if operation == "export_status":
        return "python exec " + call
    return "python exec __import__('capture_native_acquisition').NativeCaptureFrames.emit(" + call + ")"


def result_value(result, parse_transport):
    """Retain SOAP failure fields and parse only a complete NativeCaptureFrames receipt."""
    required = {"http_status", "ok", "response", "soap_fault", "receipt_truncated"}
    if type(result) is not dict or not required <= set(result):
        raise ValueError("incomplete SOAP result")
    if type(result["http_status"]) is not int or result["http_status"] != 200:
        raise ValueError("SOAP HTTP status refused: " + str(result["http_status"]))
    if result["ok"] is not True or result["soap_fault"] is not None or result["receipt_truncated"] is not False:
        raise ValueError("SOAP fault, command refusal, or truncated receipt")
    if not isinstance(result["response"], str) or not result["response"]:
        raise ValueError("SOAP response missing")
    return parse_transport(result["response"])


def execute(operation, args, credentials, soap_command, parse_transport):
    """Bind the accepted SOAP helper to a fixed wrapper call, without a command escape hatch."""
    return result_value(soap_command(command(operation, args), credentials), parse_transport)
