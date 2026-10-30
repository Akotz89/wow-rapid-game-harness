"""Exercise actual live Go orchestration with synthetic protocol/API boundaries."""
from concurrent.futures import ThreadPoolExecutor
import json
import math
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time
import unittest


HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
FULL = '^Test(LiveGameplay|PinnedExactNameCreationPolicy)$'
SMOKE = FULL + '/^(Login|ChatEcho|NativeMovement|LearnedSelfCast)$'

CLIENT = r'''package client
import("encoding/binary";"encoding/json";"errors";"math";"os";"strings";"time")
const(SmsgLogoutComplete uint16=1;SmsgMessageChat uint16=2;SmsgSpellGo uint16=0x132;SmsgSpellFailure uint16=0x133;SmsgCastFailed uint16=0x130;ChatMsgSay=1;LangCommon=0)
type WorldClient struct{hook func(uint16,[]byte);spellPacket []byte;spell func(uint32,bool,uint8);logoutAck bool;logoutErr error}
func State()map[string]any{b,e:=os.ReadFile(os.Getenv("FAKE_STATE"));if e!=nil{panic(e)};m:=map[string]any{};if e=json.Unmarshal(b,&m);e!=nil{panic(e)};return m}
func Save(m map[string]any){b,e:=json.Marshal(m);if e!=nil{panic(e)};if e=os.WriteFile(os.Getenv("FAKE_STATE"),b,0600);e!=nil{panic(e)}}
func bump(m map[string]any,k string)float64{n,_:=m[k].(float64);n++;m[k]=n;return n}
func mode()string{return os.Getenv("FAKE_MODE")}
func trace(m map[string]any,kind string,x,y,z,o float32){a,_:=m["events"].([]any);m["events"]=append(a,map[string]any{"kind":kind,"x":x,"y":y,"z":z,"facing":o,"actual_x":m["x"],"actual_y":m["y"],"actual_z":m["z"]})}
func wrong(m map[string]any,why string)error{a,_:=m["violations"].([]any);m["violations"]=append(a,why);Save(m);return errors.New(why)}
func same(a,b float64)bool{return math.Abs(a-b)<0.00001}
func position(m map[string]any,x,y,z float32)bool{return same(float64(x),m["x"].(float64))&&same(float64(y),m["y"].(float64))&&same(float64(z),m["z"].(float64))}
func(w *WorldClient)IsInWorld()bool{return State()["session_outworld"]!=true}
func(w *WorldClient)Position()(float32,float32,float32,float32,uint32){m:=State();o:=float32(0.7);if mode()=="nan_facing"{o=float32(math.NaN())};if mode()=="infinite_facing"{o=float32(math.Inf(1))};return float32(m["x"].(float64)),float32(m["y"].(float64)),float32(m["z"].(float64)),o,0}
func(w *WorldClient)AddPacketHook(f func(uint16,[]byte))func(){w.hook=f;return func(){w.hook=nil;if mode()=="spell_cancel_inflight"&&w.spellPacket!=nil{for i:=0;i<3;i++{f(SmsgSpellGo,w.spellPacket)}}}}
func(w *WorldClient)SendLogout()error{
 m:=State();n:=bump(m,"logouts");trace(m,"logout",0,0,0,0)
 if mode()=="logout_error"{Save(m);return errors.New("synthetic logout error")}
 m["saved_x"]=m["x"];m["saved_y"]=m["y"];m["saved_z"]=m["z"];m["online"]=false
 if (mode()=="final_logout_no_ack"&&n==2)||(mode()=="first_logout_no_ack"&&n==1){Save(m);return nil}
 if (mode()=="final_logout_disconnect"&&n==2)||(mode()=="first_logout_disconnect"&&n==1){w.logoutErr=errors.New("synthetic terminal disconnect");Save(m);return nil}
 w.logoutAck=true;bump(m,"logout_acks");Save(m);if w.hook!=nil{w.hook(SmsgLogoutComplete,nil)};return nil
}
func(w *WorldClient)WaitForLogout(timeout time.Duration)error{if w.logoutAck{return nil};if w.logoutErr!=nil{return w.logoutErr};time.Sleep(timeout);return errors.New("logout timeout")}
func(w *WorldClient)SendChatMessage(a,b int,s string)error{p:=make([]byte,31+len(s));p[0]=1;binary.LittleEndian.PutUint64(p[5:13],uint64(State()["guid"].(float64)));binary.LittleEndian.PutUint32(p[25:29],uint32(len(s)+1));copy(p[29:],s);w.hook(SmsgMessageChat,p);return nil}
func(w *WorldClient)MoveForwardAtTime(x,y,z,o float32,t time.Time)error{
 m:=State();n:=bump(m,"starts");trace(m,"start",x,y,z,o);m["start_ns"]=float64(t.UnixNano())
 if !position(m,x,y,z){return wrong(m,"start origin differs from authenticated current position")}
 if n>1{want:=math.Atan2(-m["y"].(float64),-m["x"].(float64));delta:=math.Atan2(math.Sin(float64(o)-want),math.Cos(float64(o)-want));if math.Abs(delta)>0.00001{return wrong(m,"return facing points away from original origin")}}
 m["return_failed"]=false
 if n==1&&(mode()=="unknown_start"||strings.HasPrefix(mode(),"near_start")){
  if mode()=="unknown_start"{m["x"]=1.5}else{m["x"]=0.05};Save(m);return errors.New("synthetic unknown write outcome")
 }
 if n>1&&mode()=="return_start_error"{m["return_failed"]=true;Save(m);return errors.New("synthetic return start error")}
 Save(m);return nil
}
func(w *WorldClient)MoveStopAtTime(x,y,z,o float32,t time.Time)error{
 m:=State();stop:=bump(m,"stops");n:=m["starts"].(float64);trace(m,"stop",x,y,z,o)
 safety:=m["return_failed"]==true||(n==1&&(mode()=="unknown_start"||strings.HasPrefix(mode(),"near_start")||(mode()=="initial_stop_error"&&stop>1)))
 if safety{if !position(m,x,y,z){return wrong(m,"safety stop differs from fresh authenticated position")}}else if n>1{if !same(float64(x),0)||!same(float64(y),0)||!same(float64(z),0){return wrong(m,"return endpoint differs from original origin")}}else{if !same(float64(x),math.Cos(0.7))||!same(float64(y),math.Sin(0.7)){return wrong(m,"outbound endpoint ignores nonzero facing")}}
 if n==1&&mode()=="initial_stop_error"&&stop==1{m["x"]=0.6;m["y"]=0.2;Save(m);return errors.New("synthetic initial stop error")}
 if n==1&&((mode()=="near_start_retry"&&stop==1)||mode()=="near_start_unresolved"){Save(m);return errors.New("synthetic unresolved stop")}
 if n>1&&strings.HasPrefix(mode(),"return_stop_")&&(!safety||mode()=="return_stop_error"){
  m["return_failed"]=true
  if mode()=="return_stop_refresh"{m["x"]=0.4;m["y"]=0.3}
  if mode()=="return_stop_outage"{m["unavailable"]=true}
  if mode()=="return_stop_foreign"{m["guid"]=43.0}
  Save(m);return errors.New("synthetic return stop error")
 }
 m["x"]=float64(x);m["y"]=float64(y);m["z"]=float64(z);m["elapsed_ms"]=(float64(t.UnixNano())-m["start_ns"].(float64))/1e6
 if n==1&&mode()=="displaced_failure"{m["x"]=1.5;m["y"]=0.0}
 if n>1&&mode()=="return_observation_error"{m["unavailable"]=true}
 Save(m);return nil
}
func(w *WorldClient)KnowsSpell(id uint32)bool{return true}
func(w *WorldClient)AddSpellCastResultHook(f func(uint32,bool,uint8))func(){w.spell=f;return func(){w.spell=nil}}
func packed(value uint64)[]byte{p:=[]byte{0};for i:=0;i<8;i++{if b:=byte(value>>(8*i));b!=0{p[0]|=1<<i;p=append(p,b)}};return p}
func(w *WorldClient)CastSpell(id uint32,guid uint64)error{
 m:=State();trace(m,"cast",0,0,0,0);Save(m)
 if strings.HasPrefix(mode(),"spell_cast_failed"){
  reason:=byte(0);args:=0
  if mode()=="spell_cast_failed_args4"{reason=102;args=4};if mode()=="spell_cast_failed_args8"{reason=29;args=8}
  p:=binary.LittleEndian.AppendUint32([]byte{0},id);p=append(p,reason);p=append(p,make([]byte,args)...)
  if w.hook!=nil{w.hook(SmsgCastFailed,p)};return nil
 }
 target:=guid;if mode()=="spell_foreign"{target++}
 p:=append(packed(guid),packed(guid)...);p=append(p,0);p=binary.LittleEndian.AppendUint32(p,id);p=binary.LittleEndian.AppendUint32(p,0x900);p=binary.LittleEndian.AppendUint32(p,123)
 p=append(p,1);p=binary.LittleEndian.AppendUint64(p,target);p=append(p,0);p=binary.LittleEndian.AppendUint32(p,2);p=append(p,packed(target)...);p=binary.LittleEndian.AppendUint32(p,88)
 if mode()=="spell_malformed"{p=p[:len(p)-1]}
 w.spellPacket=p;if w.hook!=nil{w.hook(SmsgSpellGo,p)}
 if w.spell!=nil{w.spell(id,true,0)}
 if mode()=="spell_foreign"||mode()=="spell_malformed"{p=append(packed(guid),0);p=binary.LittleEndian.AppendUint32(p,id);p=append(p,40);if w.hook!=nil{w.hook(SmsgSpellFailure,p)}}
 return nil
}
func(w *WorldClient)Close(){m:=State();bump(m,"closes");trace(m,"close",0,0,0,0);Save(m)}
'''

SESSION = r'''package e2eharness
import("errors";"os";"testing";"github.com/azerothcore/AzerothGhost/client")
type Session struct{World *client.WorldClient;Name string;GUID uint64}
type LoginOptions struct{AuthAddr,User,Password,CharName string;Race,Class uint8}
func LoginBot(t *testing.T,o LoginOptions)(*Session,error){m:=client.State();n,_:=m["logins"].(float64);n++;m["logins"]=n;if n==2&&os.Getenv("FAKE_MODE")=="relog_error"{client.Save(m);return nil,errors.New("synthetic relog failure")};m["online"]=true;client.Save(m);name:=o.CharName;if os.Getenv("FAKE_MODE")=="initial_identity_error"{name="Other"};return &Session{World:&client.WorldClient{},Name:name,GUID:uint64(m["guid"].(float64))},nil}
func(s *Session)Close(){s.World.Close()}
'''

BOUNDS = r'''package harness
import("math";"testing";"github.com/azerothcore/AzerothGhost/client";"github.com/azerothcore/AzerothGhost/e2e/e2eharness")
func TestReturnToStartBounds(t *testing.T){
 initial:=observation{Name:"Rhcanary",GUID:42,Map:0,Level:1,Health:100,Online:true}
 for _,test:=range []struct{name string;alter func(map[string]any);bad bool;move bool}{
  {"already_restored",func(m map[string]any){m["x"]=0.0},false,false},
  {"near_start",func(m map[string]any){m["x"]=0.14},false,false},
  {"local_return",func(m map[string]any){},false,true},
  {"at_bound",func(m map[string]any){m["x"]=2.0},false,true},
  {"wrong_name",func(m map[string]any){m["name"]="Other"},true,false},
  {"wrong_guid",func(m map[string]any){m["guid"]=43.0},true,false},
  {"wrong_map",func(m map[string]any){m["map"]=1.0},true,false},
  {"dead",func(m map[string]any){m["health"]=0.0},true,false},
  {"progressed",func(m map[string]any){m["level"]=2.0},true,false},
  {"offline",func(m map[string]any){m["online"]=false},true,false},
  {"outside_bound",func(m map[string]any){m["x"]=2.01},true,false},
  {"api_outage",func(m map[string]any){m["unavailable"]=true},true,false},
  {"session_not_in_world",func(m map[string]any){m["session_outworld"]=true},true,false},
 }{t.Run(test.name,func(t *testing.T){
  m:=map[string]any{"name":"Rhcanary","guid":42.0,"map":0.0,"x":1.5,"y":0.0,"z":0.0,"level":1.0,"health":100.0,"mana":100.0,"online":true,"starts":1.0,"stops":0.0};test.alter(m);client.Save(m)
  session:=&e2eharness.Session{World:&client.WorldClient{},Name:initial.Name,GUID:initial.GUID}
  returned,err:=returnToStart(session,initial,false);if (err!=nil)!=test.bad{t.Fatalf("error=%v bad=%v",err,test.bad)}
  got:=client.State();wantStarts:=1.0;if test.move{wantStarts++};if got["starts"]!=wantStarts{t.Fatalf("unexpected movement: %v",got)}
  if !test.move&&got["stops"]!=0.0{t.Fatal("unnecessary stop sent")}
  if test.move{if persisted(initial,returned)!=nil{t.Fatal("arrival not observed")};if got["elapsed_ms"].(float64)<math.Ceil(m["x"].(float64)*200){t.Fatal("return duration too short")}}
 })}
 for _,name:=range []string{"name","guid"}{t.Run("session_"+name,func(t *testing.T){
  m:=client.State();delete(m,"session_outworld");client.Save(m)
  session:=&e2eharness.Session{World:&client.WorldClient{},Name:initial.Name,GUID:initial.GUID};if name=="name"{session.Name="Other"}else{session.GUID++}
  if _,err:=returnToStart(session,initial,false);err==nil{t.Fatal("foreign session accepted")}
 })}
}
'''

REMOTE = '''import json, os, sys
if sys.argv[1] == "observe":
    s = json.loads(open(os.environ["FAKE_STATE"]).read())
    if s.get("unavailable"):
        sys.exit(1)
    print(json.dumps({k: s[k] for k in ("name", "guid", "map", "x", "y", "z", "level", "health", "mana", "online")}))
'''


class NativeCleanupTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        subprocess.run([str(ROOT / "scripts/wow-build-host-guard")], check=True, capture_output=True)
        task = ROOT / "work/rapid-game-harness-20261012"
        cls.temp = tempfile.TemporaryDirectory(prefix="synthetic-recovery-", dir=task)
        cls.addClassCleanup(cls.temp.cleanup)
        cls.box = Path(cls.temp.name)
        for name in ("harness_test.go", "live_test.go", "dependency_policy_test.go"):
            shutil.copyfile(HERE / name, cls.box / name)
        policy = Path('.deps/azerothghost/e2e/e2eharness/session.go')
        (cls.box / policy).parent.mkdir(parents=True)
        shutil.copyfile(HERE / policy, cls.box / policy)
        files = {
            "go.mod": "module synthetic-recovery\n\ngo 1.26.0\nrequire github.com/azerothcore/AzerothGhost v0.0.0\nreplace github.com/azerothcore/AzerothGhost => ./fake\n",
            "fake/go.mod": "module github.com/azerothcore/AzerothGhost\n\ngo 1.26.0\n",
            "fake/client/client.go": CLIENT,
            "fake/e2e/e2eharness/session.go": SESSION,
            "bounds_test.go": BOUNDS,
            "remote.py": REMOTE,
            "config.py": 'import json\nprint(json.dumps({"auth_addr":"127.0.0.1:1","username":"RHCANARY","password":"synthetic","character":"Rhcanary"}))\n',
        }
        for name, data in files.items():
            dest = cls.box / name
            dest.parent.mkdir(parents=True, exist_ok=True)
            dest.write_text(data)
        cls.env = dict(os.environ, GOTOOLCHAIN="local", GOPROXY="off", GOSUMDB="off",
                       WOW_TEST_CONFIG="synthetic-only")
        build = subprocess.run([str(task / "go/bin/go"), "test", "-c", "-tags=live", "-o", str(cls.box / "harness.test"), "."],
                               cwd=cls.box, env=cls.env, capture_output=True, text=True, timeout=45)
        if build.returncode:
            raise AssertionError(build.stdout + build.stderr)

    def execute_case(self, mode, test=FULL):
        state = dict(name="Rhcanary", guid=1120 if mode.startswith("spell_") else 42, map=0, x=0, y=0, z=0, level=1,
                     health=100, mana=100, online=True, starts=0, stops=0, logouts=0, closes=0)
        with tempfile.TemporaryDirectory(prefix="case-", dir=self.box) as case:
            state_path = Path(case) / "state.json"
            state_path.write_text(json.dumps(state))
            start = time.monotonic()
            result = subprocess.run([str(self.box / "harness.test"), "-test.run=" + test,
                                     "-test.v", "-test.timeout=40s"], cwd=self.box,
                                    env=dict(self.env, FAKE_MODE=mode, FAKE_STATE=str(state_path)),
                                    capture_output=True, text=True, timeout=45)
            elapsed = time.monotonic() - start
            output = result.stdout + result.stderr
            try:
                state = json.loads(state_path.read_text())
            except (OSError, json.JSONDecodeError) as error:
                raise RuntimeError("Cannot load synthetic case state:\n" + output) from error
        return result, state, output, elapsed

    def check_case(self, execution, failed=False):
        result, state, output, elapsed = execution
        self.assertEqual(result.returncode != 0, failed, output)
        self.assertNotIn("panic:", output)
        self.assertFalse(state.get("violations"), state)
        return state, output, elapsed

    def run_case(self, mode, test=FULL, failed=False):
        return self.check_case(self.execute_case(mode, test), failed)[:2]

    def test_native_bounds_and_noop(self):
        self.run_case("", "^TestReturnToStartBounds$")

    def test_actual_parent_cleanup(self):
        cases = (
            ("normal", False, 2, 2, 2, "native return_to_start_delta_m="),
            ("displaced_failure", True, 2, 1, 1, "--- FAIL: TestLiveGameplay/NativeMovement"),
            ("unknown_start", True, 2, 1, 1, "movement start failed"),
            ("initial_stop_error", True, 2, 1, 1, "movement stop failed"),
            ("near_start", True, 1, 1, 1, "movement start failed"),
            ("near_start_retry", True, 1, 1, 1, "outbound movement safety stop failed"),
            ("near_start_unresolved", True, 1, 1, 1, "unresolved movement safety stop failed"),
            ("return_start_error", True, 2, 2, 2, "return movement start failed"),
            ("return_stop_error", True, 2, 2, 2, "return movement safety stop failed"),
            ("return_stop_refresh", True, 2, 2, 2, "return movement stop failed"),
            ("return_stop_outage", True, 2, 2, 2, "return movement safety stop failed"),
            ("return_stop_foreign", True, 2, 2, 2, "return movement safety stop failed"),
            ("return_observation_error", True, 2, 2, 2, "native return-to-start observation failed"),
            ("initial_identity_error", True, 0, 1, 1, "login prerequisite failed"),
            ("relog_error", True, 1, 1, 1, "no usable session"),
            ("logout_error", True, 2, 2, 1, "logout request failed"),
            ("nan_facing", True, 0, 1, 1, "native movement orientation is not finite"),
            ("infinite_facing", True, 0, 1, 1, "native movement orientation is not finite"),
            ("final_logout_no_ack", True, 2, 2, 2, "native logout completion failed: logout timeout"),
            ("final_logout_disconnect", True, 2, 2, 2, "native logout completion failed: synthetic terminal disconnect"),
        )
        with ThreadPoolExecutor(max_workers=4) as pool:
            futures = [pool.submit(self.execute_case, case[0]) for case in cases]
            for (mode, failed, starts, logouts, closes, marker), future in zip(cases, futures):
                with self.subTest(mode=mode):
                    state, output, elapsed = self.check_case(future.result(), failed)
                    if mode == "final_logout_disconnect":
                        self.assertLess(elapsed, 10, output)
                    if mode == "final_logout_no_ack":
                        self.assertGreaterEqual(elapsed, 25, output)
                    self.assertIn(marker, output)
                    if mode == 'normal':
                        self.assertIn('--- PASS: TestLiveGameplay (', output)
                        self.assertIn('--- PASS: TestPinnedExactNameCreationPolicy', output)
                        for child in ('Login', 'ChatEcho', 'NativeMovement', 'LearnedSelfCast', 'LogoutRelogPersistence'):
                            self.assertIn('--- PASS: TestLiveGameplay/' + child + ' (', output)
                    self.assertEqual((state["starts"], state["logouts"], state["closes"]),
                                     (starts, logouts, closes), output)
                    events = state.get("events", [])
                    if mode not in ("logout_error", "final_logout_no_ack", "final_logout_disconnect"):
                        self.assertEqual(state.get("logout_acks", 0), logouts)
                    if mode in ("final_logout_no_ack", "final_logout_disconnect"):
                        self.assertEqual(state["logout_acks"], 1)
                    if mode in ("normal", "displaced_failure", "unknown_start"):
                        self.assertIn("native return_to_start_delta_m=0.0000", output)
                        self.assertEqual((state["x"], state["y"], state["saved_x"], state["saved_y"], state["online"]),
                                         (0, 0, 0, 0, False))
                    if mode in ("displaced_failure", "unknown_start", "initial_stop_error") or mode.startswith("near_start"):
                        self.assertIn("native movement prerequisite failed", output)
                    if mode in ("unknown_start", "initial_stop_error") or mode.startswith("near_start"):
                        kinds = [event["kind"] for event in events]
                        self.assertLess(kinds.index("stop"), kinds.index("cast"))
                        self.assertIn("--- FAIL: TestLiveGameplay/NativeMovement", output)
                    if mode in ("near_start", "near_start_retry"):
                        self.assertEqual(state["stops"], 1 if mode == "near_start" else 2)
                        self.assertAlmostEqual(state["saved_x"], 0.05, places=6)
                        self.assertIn("native return_to_start_delta_m=0.0500", output)
                    if mode == "near_start_unresolved":
                        self.assertEqual(state["stops"], 2)
                        self.assertNotIn("native return_to_start_delta_m=", output)
                    if mode in ("return_start_error", "return_stop_error"):
                        self.assertGreaterEqual(state["stops"], 2)
                    if mode == "return_start_error":
                        self.assertAlmostEqual(state["saved_x"], math.cos(0.7), places=6)
                        self.assertAlmostEqual(state["saved_y"], math.sin(0.7), places=6)
                        stop = [event for event in events if event["kind"] == "stop"][-1]
                        self.assertAlmostEqual(stop["x"], stop["actual_x"], places=6)
                        self.assertAlmostEqual(stop["y"], stop["actual_y"], places=6)
                    if mode == "return_stop_refresh":
                        self.assertEqual(state["stops"], 3)
                        self.assertAlmostEqual(state["saved_x"], 0.4, places=6)
                        self.assertAlmostEqual(state["saved_y"], 0.3, places=6)
                        stop = [event for event in events if event["kind"] == "stop"][-1]
                        self.assertAlmostEqual(stop["x"], 0.4, places=6)
                        self.assertAlmostEqual(stop["y"], 0.3, places=6)
                    if mode in ("return_stop_outage", "return_stop_foreign"):
                        self.assertEqual(state["stops"], 2)
                    if mode in ("return_start_error", "return_observation_error") or mode.startswith("return_stop_"):
                        self.assertNotIn("native return_to_start_delta_m=", output)

    def test_smoke_parent_cleanup(self):
        children = ('Login', 'ChatEcho', 'NativeMovement', 'LearnedSelfCast')
        cases = (
            ('normal', False, 1, 'native return_to_start_delta_m=0.0000'),
            ('unknown_start', True, 1, 'movement start failed'),
            ('logout_error', True, 0, 'logout request failed'),
            ('first_logout_disconnect', True, 0, 'native logout completion failed: synthetic terminal disconnect'),
            ('first_logout_no_ack', True, 0, 'native logout completion failed: logout timeout'),
        )
        with ThreadPoolExecutor(max_workers=4) as pool:
            futures = [pool.submit(self.execute_case, case[0], SMOKE) for case in cases]
            for (mode, failed, acknowledgments, marker), future in zip(cases, futures):
                with self.subTest(mode=mode):
                    state, output, elapsed = self.check_case(future.result(), failed)
                    if mode == 'first_logout_disconnect':
                        self.assertLess(elapsed, 10, output)
                    if mode == 'first_logout_no_ack':
                        self.assertGreaterEqual(elapsed, 25, output)
                    self.assertIn(marker, output)
                    self.assertEqual((state['starts'], state['logouts'], state['closes']), (2, 1, 1), output)
                    self.assertEqual(state.get('logout_acks', 0), acknowledgments, output)
                    self.assertNotIn('LogoutRelogPersistence', output)
                    selected = [line.split()[-1] for line in output.splitlines()
                                if line.startswith('=== RUN   TestLiveGameplay/')]
                    self.assertEqual(selected, ['TestLiveGameplay/' + child for child in children])
                    self.assertIn('--- PASS: TestPinnedExactNameCreationPolicy', output)
                    self.assertIn(('--- FAIL:' if failed else '--- PASS:') + ' TestLiveGameplay (', output)
                    for child in children:
                        action = 'FAIL' if mode == 'unknown_start' and child == 'NativeMovement' else 'PASS'
                        self.assertIn('--- ' + action + ': TestLiveGameplay/' + child + ' (', output)
                    self.assertIn('native return_to_start_delta_m=0.0000', output)
                    self.assertEqual((state['x'], state['y'], state['z']), (0, 0, 0))
                    if mode != 'logout_error':
                        self.assertEqual((state['saved_x'], state['saved_y'], state['saved_z'], state['online']),
                                         (0, 0, 0, False))
                    kinds = [event['kind'] for event in state['events']]
                    self.assertLess(max(i for i, kind in enumerate(kinds) if kind == 'stop'), kinds.index('logout'))
                    last_stop = [event for event in state['events'] if event['kind'] == 'stop'][-1]
                    self.assertEqual((last_stop['x'], last_stop['y'], last_stop['z']), (0, 0, 0))
                    self.assertEqual(kinds[-2:], ['logout', 'close'])

    def test_spell_receipts_cannot_be_bypassed(self):
        selected = FULL + '/^(Login|LearnedSelfCast)$'
        for mode, reason in (('spell_foreign', 40), ('spell_malformed', 40),
                             ('spell_cancel_inflight', None), ('spell_cast_failed_zero', 0),
                             ('spell_cast_failed_args4', 102), ('spell_cast_failed_args8', 29)):
            with self.subTest(mode=mode):
                failed = mode != 'spell_cancel_inflight'
                start = time.monotonic()
                state, output = self.run_case(mode, selected, failed)
                self.assertLess(time.monotonic() - start, 5, output)
                self.assertIn('--- PASS: TestPinnedExactNameCreationPolicy', output)
                self.assertIn(('--- FAIL:' if failed else '--- PASS:') + ' TestLiveGameplay (', output)
                self.assertEqual((state['starts'], state['logouts'], state['logout_acks'], state['closes']),
                                 (0, 1, 1, 1), output)
                self.assertEqual([e['kind'] for e in state['events']][-2:], ['logout', 'close'])
                if failed:
                    self.assertIn('native cast rejected reason=' + str(reason), output)
                    self.assertIn('--- FAIL: TestLiveGameplay/LearnedSelfCast (', output)
                    self.assertNotIn('--- PASS: TestLiveGameplay/LearnedSelfCast (', output)
                    self.assertNotIn('validated native spell=', output)
                else:
                    self.assertIn('validated native spell=2050 self_target_guid=1120 cast_count=0', output)
        state, output = self.run_case('spell_foreign', SMOKE, failed=True)
        self.assertIn('native cast rejected reason=40', output)
        self.assertIn('native return_to_start_delta_m=0.0000', output)
        self.assertEqual((state['starts'], state['logouts'], state['logout_acks'], state['closes']),
                         (2, 1, 1, 1), output)
        self.assertEqual((state['x'], state['y'], state['saved_x'], state['saved_y'], state['online']),
                         (0, 0, 0, 0, False))
        self.assertIn('--- FAIL: TestLiveGameplay (', output)
        self.assertIn('--- PASS: TestPinnedExactNameCreationPolicy', output)
        self.assertNotIn('--- PASS: TestLiveGameplay/LearnedSelfCast (', output)

    def test_no_movement_and_no_origin(self):
        for test, failed, logouts in (("^TestLiveGameplay/Login$", False, 1),
                                      ("^TestLiveGameplay/ChatEcho$", True, 0)):
            with self.subTest(test=test):
                state, _ = self.run_case("normal", test, failed)
                self.assertEqual(state["starts"], 0)
                self.assertEqual(state["logouts"], logouts)
                self.assertEqual(state["closes"], logouts)

    def test_smoke_original_session_failures(self):
        for mode in ('return_stop_error', 'initial_identity_error'):
            with self.subTest(mode=mode):
                state, output = self.run_case(mode, SMOKE, failed=True)
                self.assertIn('--- FAIL: TestLiveGameplay (', output)
                self.assertIn('--- PASS: TestPinnedExactNameCreationPolicy', output)
                self.assertNotIn('LogoutRelogPersistence', output)
                self.assertNotIn('native return_to_start_delta_m=', output)
                self.assertEqual((state['logins'], state['logouts'], state['logout_acks'], state['closes']),
                                 (1, 1, 1, 1), output)
                self.assertFalse(state['online'])
                events = state['events']
                self.assertEqual([event['kind'] for event in events][-2:], ['logout', 'close'])
                if mode == 'initial_identity_error':
                    self.assertEqual((state['starts'], state['stops']), (0, 0))
                    self.assertIn('native session identity or phase mismatch', output)
                    self.assertIn('login prerequisite failed', output)
                    self.assertEqual((state['saved_x'], state['saved_y'], state['saved_z']), (0, 0, 0))
                    for child in ('Login', 'ChatEcho', 'NativeMovement', 'LearnedSelfCast'):
                        self.assertIn('--- FAIL: TestLiveGameplay/' + child + ' (', output)
                else:
                    self.assertIn('return movement safety stop failed', output)
                    self.assertEqual((state['starts'], state['stops']), (2, 3))
                    for child in ('Login', 'ChatEcho', 'NativeMovement', 'LearnedSelfCast'):
                        self.assertIn('--- PASS: TestLiveGameplay/' + child + ' (', output)
                    stop = [event for event in events if event['kind'] == 'stop'][-1]
                    self.assertAlmostEqual(stop['x'], stop['actual_x'], places=6)
                    self.assertAlmostEqual(stop['y'], stop['actual_y'], places=6)
                    self.assertAlmostEqual(state['saved_x'], math.cos(0.7), places=6)
                    self.assertAlmostEqual(state['saved_y'], math.sin(0.7), places=6)


if __name__ == "__main__":
    unittest.main()
