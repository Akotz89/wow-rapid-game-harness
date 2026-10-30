"""Execute the complete current LoginBot verbatim with stdlib-only boundary fakes."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]

CLIENT = r'''package client
import (
 "errors"
 "fmt"
 "sync"
 "strings"
 "time"
)
type RealmInfo struct { Name, Address string }
type AuthClient struct {}
func NewAuthClient(string,string) *AuthClient { return &AuthClient{} }
func (*AuthClient) Authenticate(string) ([]RealmInfo,error) { return []RealmInfo{{"fake", "in-memory"}},nil }
func (*AuthClient) SessionKey() []byte { return nil }
type CharEnumEntry struct { Name string; GUID uint64; Race uint8 }
type LogLevel int
const LogInfo LogLevel = 1
func ParseLogLevel(string) (LogLevel,bool) { return LogInfo,true }
const PhaseAuthed = 1
var Mode string
var Last *WorldClient
var Cause = errors.New("synthetic transport disconnect")
type WorldClient struct {
 OnCharList func([]CharEnumEntry)
 OnCharCreateResult func([]byte)
 stop chan struct{}
 LoginWait, LoginReceipt chan struct{}
 once sync.Once
 mu sync.Mutex
 err error
 stopped bool
 Ready, Splits int
 Requests, Creates, Logins int
 CreatedName string
 LoginGUID uint64
 Race uint8
}
func NewWorldClient(string, []byte, func(string,...interface{})) *WorldClient {
 Last = &WorldClient{stop:make(chan struct{}),LoginWait:make(chan struct{}),LoginReceipt:make(chan struct{})}; return Last
}
func (*WorldClient) SetLogLevel(LogLevel) {}
func (w *WorldClient) SetCharRace(r uint8) { w.Race = r }
func (*WorldClient) Connect(string) error { return nil }
func (w *WorldClient) Run() error { <-w.stop; return w.terminal() }
func (*WorldClient) WaitForSessionPhase(int,time.Duration) error { return nil }
func (w *WorldClient) SendReadyForAccountDataTimes() error {
 w.Ready++; if Mode=="ready_error" {w.fail();return Cause}; return nil
}
func (w *WorldClient) SendRealmSplit() error {
 w.Splits++; if Mode=="split_error" {w.fail();return Cause}; return nil
}
func (w *WorldClient) StopChan() <-chan struct{} { return w.stop }
func (w *WorldClient) fail() { w.mu.Lock();w.err=Cause;w.mu.Unlock();w.Close() }
func (w *WorldClient) laterFail() { go func(){time.Sleep(time.Second);w.fail()}() }
func (w *WorldClient) terminal() error { w.mu.Lock();defer w.mu.Unlock();return w.err }
func (w *WorldClient) Close() { w.mu.Lock();w.stopped=true;w.mu.Unlock();w.once.Do(func(){close(w.stop)}) }
func (w *WorldClient) StopError() error {w.mu.Lock();defer w.mu.Unlock();select{case <-w.stop:default:if !w.stopped{return nil}};if w.err!=nil{return w.err};return errors.New("client stopped")}
func (w *WorldClient) receipt(stage string, deliver func()) bool {
 if !strings.HasPrefix(Mode,stage+"_"){return false}
 suffix:=strings.TrimPrefix(Mode,stage+"_")
 if suffix!="buffered_stop"&&suffix!="buffered_gap"&&suffix!="buffered_channel"&&suffix!="queued_stop"&&suffix!="queued_gap"&&suffix!="stopped_before_wait"{return false}
 run:=func(){
  w.mu.Lock()
  if suffix!="stopped_before_wait"{deliver()}
  w.err=Cause
  if suffix!="buffered_channel"{w.stopped=true}
  w.mu.Unlock()
  if suffix!="buffered_gap"&&suffix!="queued_gap"{w.once.Do(func(){close(w.stop)})}
 }
 if strings.HasPrefix(suffix,"queued_"){go func(){time.Sleep(time.Second);run()}()}else{run()};return true
}

func (w *WorldClient) RequestCharList() error {
 w.Requests++
 stage:="enum";if w.Requests>1{stage="post"}
 if Mode==stage+"_send_race"{w.fail();return errors.New("not connected or stopped")}
 if err:=w.terminal();err!=nil{return err}
 list:=[]CharEnumEntry{{Name:"Wanted",GUID:42,Race:2}}
 if w.receipt(stage,func(){w.OnCharList(list)}){return nil}
 switch Mode {
 case "enum_disconnect": w.laterFail()
 case "enum_unknown":
 case "enum_buffered_stop": w.OnCharList(list);w.fail()
 case "healthy_existing": w.OnCharList(list)
 default:
  if w.Requests==1 {w.OnCharList([]CharEnumEntry{{Name:"Other",GUID:1}})
  } else if Mode=="post_disconnect" {w.laterFail()
  } else if Mode=="post_wrong_name" {w.OnCharList([]CharEnumEntry{{Name:"Other",GUID:999}})
  } else if Mode=="post_unknown" {
  } else if Mode=="post_buffered_stop" {w.OnCharList(list);w.fail()
  } else {w.OnCharList(list)}
 }
 return nil
}
func (w *WorldClient) CreateCharacter(name string, a,b,c,d,e,f,g,h,i uint8) error {
 w.Creates++;w.CreatedName=name
 if Mode=="create_send_race"{w.fail();return errors.New("not connected or stopped")}
 if w.receipt("create",func(){w.OnCharCreateResult([]byte{0x2f})}){return nil}
 if err:=w.terminal();err!=nil{return err}
 switch Mode {
 case "create_disconnect": w.laterFail()
 case "create_unknown":
 case "create_buffered_stop": w.OnCharCreateResult([]byte{0x2f});w.fail()
 default: w.OnCharCreateResult([]byte{0x2f})
 }
 return nil
}
func (w *WorldClient) LoginCharacter(guid uint64) error {
 w.Logins++;w.LoginGUID=guid
 if Mode=="login_send_race"{w.fail();return errors.New("not connected or stopped")}
 return w.terminal()
}
func (w *WorldClient) WaitForLogin(time.Duration) error {
 if Mode=="healthy_delayed_login"||Mode=="login_wait_failure"{
  close(w.LoginWait);<-w.LoginReceipt
  if Mode=="login_wait_failure"{return fmt.Errorf("synthetic native login receipt failure: %w",Cause)}
 }
 if err:=w.terminal();err!=nil{return fmt.Errorf("client stopped while waiting for login verify world: %w",err)}
 return nil
}
'''

PREFIX = r'''package e2eharness
import ("fmt"; "os"; "strings"; "sync/atomic"; "testing"; "time"; "github.com/azerothcore/AzerothGhost/client")
var AuthAddr,DefaultPassword string
type Session struct { World *client.WorldClient; GUID uint64; Name,User string; logf func(string,...interface{}) }
type LoginOptions struct { AuthAddr,User,Password,CharName string; Race,Class uint8 }
'''

TESTS = r'''package e2eharness
import (
 "errors"
 "strings"
 "testing"
 "testing/synctest"
 "time"
 "github.com/azerothcore/AzerothGhost/client"
)
func TestActualLoginBotTerminal(t *testing.T) {
 for _,stage:=range []string{"enum","create","post"}{
  for _,mode:=range []string{"disconnect","buffered_stop","buffered_gap","buffered_channel","queued_stop","queued_gap","stopped_before_wait","send_race"}{
   t.Run(stage+"_"+mode,func(t *testing.T){synctest.Test(t,func(t *testing.T){
    client.Mode=stage+"_"+mode
    start:=time.Now()
    s,err:=LoginBot(t,LoginOptions{User:"synthetic",CharName:"wanted"})
    w:=client.Last;defer w.Close()
    wantDelay:=time.Duration(0);if mode=="disconnect"||strings.HasPrefix(mode,"queued_"){wantDelay=time.Second}
    if time.Since(start)!=wantDelay{t.Fatalf("elapsed=%v want=%v",time.Since(start),wantDelay)}
    if s!=nil||!errors.Is(err,client.Cause){t.Fatalf("terminal cause lost: session=%+v err=%v",s,err)}
    context:=map[string]string{"enum":"char enum","create":"creat","post":"char enum after create"}[stage]
    if !strings.Contains(err.Error(),context){t.Fatalf("stage context lost: %v",err)}
    requests,creates:=1,0;if stage!="enum"{creates=1};if stage=="post"{requests=2}
    if w.Requests!=requests||w.Creates!=creates||w.Logins!=0{t.Fatalf("sends past terminal: enum/create/login=%d/%d/%d",w.Requests,w.Creates,w.Logins)}
    if w.Creates>0&&w.CreatedName!="wanted"{t.Fatalf("unrequested name=%q",w.CreatedName)}
    select{case <-w.StopChan():default:t.Fatal("failed LoginBot did not close client")}
   })})
  }
 }
 for _,mode:=range []string{"ready_error","split_error","login_send_race"}{
  t.Run(mode,func(t *testing.T){synctest.Test(t,func(t *testing.T){
   client.Mode=mode;start:=time.Now()
   s,err:=LoginBot(t,LoginOptions{User:"synthetic",CharName:"wanted"})
   w:=client.Last;defer w.Close()
   if time.Since(start)!=0||s!=nil||!errors.Is(err,client.Cause){t.Fatalf("setup/send cause lost: session=%+v err=%v",s,err)}
   context:="login";requests,creates,logins,splits:=2,1,1,1
   if mode!="login_send_race"{requests,creates,logins=0,0,0;context="realm split"}
   if mode=="ready_error"{splits=0;context="account data times"}
   if !strings.Contains(err.Error(),context){t.Fatalf("context lost: %v",err)}
   if w.Requests!=requests||w.Creates!=creates||w.Logins!=logins||w.Ready!=1||w.Splits!=splits{t.Fatalf("later send after failure: %+v",w)}
   select{case <-w.StopChan():default:t.Fatal("failure did not close client")}
  })})
 }
}
func TestActualLoginBotHealthy(t *testing.T) {
 cases:=[]struct{name string;delay time.Duration;err string;requests,creates,logins int}{
  {"enum_unknown",15*time.Second,"timeout char enum",1,0,0},
  {"create_unknown",20*time.Second,"timeout creating requested character; re-enumerate on retry",1,1,0},
  {"post_unknown",15*time.Second,"timeout char enum after create",2,1,0},
  {"healthy_existing",0,"",1,0,1},
  {"healthy_create",0,"",2,1,1},
  {"post_wrong_name",0,"created char \"wanted\" not found in enum (not falling back to chars[0])",2,1,0},
 }
 for _,c:=range cases{t.Run(c.name,func(t *testing.T){synctest.Test(t,func(t *testing.T){
  client.Mode=c.name;start:=time.Now()
  s,err:=LoginBot(t,LoginOptions{User:"synthetic",CharName:"wanted"})
  w:=client.Last;defer w.Close()
  if time.Since(start)!=c.delay{t.Fatalf("elapsed=%v want=%v",time.Since(start),c.delay)}
  if c.err==""{if err!=nil||s==nil||s.GUID!=42||s.Name!="Wanted"{t.Fatalf("healthy identity failure: session=%+v err=%v",s,err)}}else{
   if err==nil||err.Error()!=c.err||s!=nil{t.Fatalf("unknown outcome=%+v err=%v want=%s",s,err,c.err)}
   select{case <-w.StopChan():default:t.Fatal("failure did not close client")}
  }
  if w.Requests!=c.requests||w.Creates!=c.creates||w.Logins!=c.logins{t.Fatalf("enum/create/login=%d/%d/%d",w.Requests,w.Creates,w.Logins)}
  if w.Creates>0&&w.CreatedName!="wanted"{t.Fatalf("created unrequested name=%q",w.CreatedName)}
  if w.Logins>0&&w.LoginGUID!=42{t.Fatalf("wrong GUID=%d",w.LoginGUID)}
 })})}
}
func TestActualLoginBotLoginReceipt(t *testing.T) {
 for _,mode:=range []string{"healthy_delayed_login","login_wait_failure"}{
  t.Run(mode,func(t *testing.T){synctest.Test(t,func(t *testing.T){
   client.Mode=mode;start:=time.Now()
   type result struct{s *Session;err error}
   done:=make(chan result,1)
   go func(){s,err:=LoginBot(t,LoginOptions{User:"synthetic",CharName:"wanted"});done<-result{s,err}}()
   synctest.Wait()
   w:=client.Last;defer w.Close()
   select{case <-w.LoginWait:default:t.Fatal("did not reach final login waiter")}
   select{case r:=<-done:t.Fatalf("returned before native login result: %+v",r);default:}
   time.Sleep(time.Second)
   close(w.LoginReceipt)
   r:=<-done
   if time.Since(start)!=time.Second{t.Fatalf("login wait elapsed=%v",time.Since(start))}
   if w.Requests!=2||w.Creates!=1||w.Logins!=1||w.LoginGUID!=42{t.Fatalf("login identity/count changed: %+v",w)}
   if mode=="healthy_delayed_login"{
    if r.err!=nil||r.s==nil||r.s.GUID!=42||r.s.Name!="Wanted"{t.Fatalf("delayed healthy login failed: %+v",r)}
    select{case <-w.StopChan():t.Fatal("healthy session was closed");default:}
   }else{
    if r.s!=nil||!errors.Is(r.err,client.Cause)||!strings.Contains(r.err.Error(),"wait login:"){t.Fatalf("login waiter cause/context lost: %+v",r)}
    select{case <-w.StopChan():default:t.Fatal("failed final login waiter did not close session")}
   }
  })})
 }
}
'''


class SessionTests(unittest.TestCase):
    def test_actual_loginbot_terminal_contract(self):
        subprocess.run([str(ROOT / 'scripts/wow-build-host-guard')], check=True, capture_output=True)
        dep = HERE / '.deps/azerothghost'
        go = ROOT / 'work/rapid-game-harness-20261012/go/bin/go'
        self.assertTrue((dep / '.git').is_dir(), 'Offline session tests need the local pinned dependency')
        self.assertTrue(go.is_file(), 'Offline session tests need local Go 1.26.9')
        env = dict(os.environ, GOENV='off', GOFLAGS='', GOWORK='off', GOTOOLCHAIN='local',
                   GOPROXY='off', GOSUMDB='off')
        env.pop('GOROOT', None)
        self.assertEqual(subprocess.check_output([str(go), 'version'], env=env, text=True).strip(),
                         'go version go1.26.9 linux/amd64')
        verified = subprocess.run([str(HERE / 'bootstrap')], env=env, capture_output=True, text=True, timeout=15)
        self.assertEqual(verified.returncode, 0, verified.stdout + verified.stderr)
        raw = (dep / 'e2e/e2eharness/session.go').read_bytes()
        start = raw.index(b'func LoginBot(')
        end = raw.index(b'\n// Close closes', start)
        function = raw[start:end]
        self.assertEqual(function.count(b'func LoginBot('), 1)
        work = ROOT / 'work/rapid-game-harness-session-terminal-20261015/implementation'
        work.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(prefix='session-', dir=work) as folder:
            box = Path(folder)
            files = {'go.mod': 'module github.com/azerothcore/AzerothGhost\n\ngo 1.26.0\n',
                     'client/fake.go': CLIENT, 'e2eharness/session_test.go': TESTS}
            for name, data in files.items():
                path = box / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(data)
            actual = box / 'e2eharness/session_actual.go'
            actual.write_bytes(PREFIX.encode() + function)
            self.assertEqual(actual.read_bytes()[len(PREFIX.encode()):], function)
            result = subprocess.run([str(go), 'test', '-count=1', '-timeout=20s', '-v',
                                     '-run=^TestActualLoginBot', './e2eharness'], cwd=box,
                                    env=env, capture_output=True, text=True, timeout=30)
            print(result.stdout + result.stderr, end='')
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            required = ['Terminal/' + stage + '_' + mode
                        for stage in ('enum', 'create', 'post')
                        for mode in ('disconnect', 'buffered_stop', 'buffered_gap', 'buffered_channel',
                                     'queued_stop', 'queued_gap', 'stopped_before_wait', 'send_race')]
            required += ['Terminal/' + mode for mode in ('ready_error', 'split_error', 'login_send_race')]
            required += ['Healthy/' + mode for mode in ('enum_unknown', 'create_unknown', 'post_unknown',
                                                        'healthy_existing', 'healthy_create', 'post_wrong_name')]
            required += ['LoginReceipt/' + mode for mode in ('healthy_delayed_login', 'login_wait_failure')]
            for name in required:
                self.assertIn('--- PASS: TestActualLoginBot' + name + ' (', result.stdout,
                              'Expected actual LoginBot case did not pass: ' + name)


if __name__ == '__main__':
    unittest.main()
