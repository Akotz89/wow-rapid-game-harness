#!/usr/bin/env python3
"""Stream Go test JSON after removing fixture credentials."""
import json
import sys
from config import validate

fixture = validate(sys.argv[1])
secrets = [fixture['password'], fixture['password'].upper(),
           fixture['username'], fixture['username'].upper()]
profile = sys.argv[2] if len(sys.argv) > 2 else None
if profile is not None and profile not in ('run', 'smoke'):
    sys.exit('Harness coverage failed: invalid profile')
children = {'Login', 'ChatEcho', 'NativeMovement', 'LearnedSelfCast'}
if profile == 'run':
    children.add('LogoutRelogPersistence')
required = {'TestLiveGameplay/' + name for name in children}
required.update({'TestLiveGameplay', 'TestPinnedExactNameCreationPolicy', ''})
actions = ('start', 'run', 'pause', 'cont', 'pass', 'bench', 'fail', 'output', 'skip')
passed = set()
invalid = False


def clean(value):
    if isinstance(value, str):
        for secret in secrets:
            value = value.replace(secret, '[REDACTED]')
        return value
    if isinstance(value, dict):
        return {key: clean(item) for key, item in value.items()}
    if isinstance(value, list):
        return [clean(item) for item in value]
    return value


for line in sys.stdin:
    try:
        event = json.loads(line)
    except json.JSONDecodeError:
        invalid = True
        print(clean(line), end='', flush=True)
        continue
    if profile is not None:
        # Check original metadata before credentials can collide with its values.
        if not isinstance(event, dict):
            invalid = True
        else:
            action, name = event.get('Action'), event.get('Test', '')
            if (not isinstance(action, str) or action not in actions
                    or event.get('Package') != 'wow/rapid-game-harness'
                    or not isinstance(name, str)
                    or (action in ('run', 'pause', 'cont', 'bench') and not name)
                    or (action == 'start' and name)):
                invalid = True
            elif (action in ('fail', 'skip')
                  or (profile == 'smoke' and name.startswith('TestLiveGameplay/') and name not in required)):
                invalid = True
            elif action == 'pass':
                passed.add(name)
    print(json.dumps(clean(event)), flush=True)

if profile is not None and (invalid or not required <= passed):
    sys.exit('Harness coverage failed: invalid or incomplete results')
