#!/usr/bin/env python3
"""Create at most ten fixture recipes from the pinned official device library."""
import hashlib
import json
from pathlib import Path
import sys
import zipfile

archive, destination = map(Path, sys.argv[1:3])
expected = '6ed51c9571b0100a5fa56b578cbc6e118b3d47eb5182759d66be4f55712bb66f'
assert hashlib.sha256(archive.read_bytes()).hexdigest() == expected
with zipfile.ZipFile(archive) as library:
    source = json.loads(library.read('shardx-fingerprints/linux-hd520.json'))
assert source['client_hints']['brand_full_version'] == '152.0.7977.65'
destination.mkdir(parents=True, exist_ok=True)
for number in range(1, 11):
    identity = f'fixture-{number:03d}'
    recipe = json.loads(json.dumps(source))
    recipe['name'] = identity
    recipe['timezone'] = 'UTC'
    recipe['icu_locale'] = 'en-US'
    recipe['navigator'].update(language='en-US', languages=['en-US', 'en'], accept_language='en-US,en;q=0.9')
    for slot, block in recipe['noise'].items():
        seed = 2166136261
        for byte in f'{identity}::{slot}'.encode():
            seed = ((seed ^ byte) * 16777619) & 0xffffffff
        block['seed'] = seed or 1
        block['enabled'] = slot not in ('sensors', 'webgl')
        if slot == 'webgl':
            block['intensity'] = 0.0005
        if slot == 'client_rects':
            block['max_offset'] = 1
    data = json.dumps(recipe, indent=2)
    path = destination / (identity + '.json')
    if path.exists():
        assert path.read_text() == data, f'Existing fixture recipe changed: {path}'
    else:
        path.write_text(data)
print('10 fixed fixture recipes verified; no browser started')
