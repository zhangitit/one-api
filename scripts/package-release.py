#!/usr/bin/env python3
"""Build or verify the deterministic ZeoNexus Gateway source distribution."""
import argparse
import hashlib
import json
import re
import stat
import tempfile
import zipfile
from datetime import date
from pathlib import Path, PurePosixPath

ROOT = Path(__file__).resolve().parents[1]
OMIT_PARTS = {'.git', '.github', '.idea', '.vscode', 'node_modules', '__pycache__', 'logs'}
OMIT_SUFFIXES = {'.pyc', '.pyo', '.log', '.key', '.pem', '.sqlite', '.db', '.bak', '.orig', '.swp'}
REQUIRED = {
    'VERSION', 'go.mod', 'go.sum', 'main.go', 'Dockerfile.zeonexus', '.dockerignore',
    'README.ZeoNexus.md', 'deploy/docker-compose.1.1.yml', 'deploy/.env.example',
    'deploy/nginx/zeonexus-gateway.conf.example',
    'docs/openapi-zeonexus-control.yaml', 'common/config/zeonexus.go',
    'middleware/zeonexus.go', 'model/zeonexus.go', 'router/zeonexus.go',
    'router/zeonexus_integration_test.go',
}


def digest(data):
    return hashlib.sha256(data).hexdigest()


def allowed(name):
    path = PurePosixPath(name)
    if path.is_absolute() or '..' in path.parts or '\\' in name:
        return False
    if any(part in OMIT_PARTS or part == '.env' or part.startswith('.env.') for part in path.parts):
        return name == 'deploy/.env.example'
    return path.suffix.lower() not in OMIT_SUFFIXES and not path.name.endswith('~')


def version():
    value = (ROOT / 'VERSION').read_text().strip()
    match = re.fullmatch(r'(\d+\.\d+\.\d+)-zeonexus', value)
    if not match:
        raise ValueError('VERSION must be major.minor.patch-zeonexus')
    return match.group(1)


def verify(archive):
    with zipfile.ZipFile(archive) as bundle:
        if bundle.testzip() is not None:
            raise ValueError('ZIP integrity check failed')
        names = bundle.namelist()
        if len(names) != len(set(names)):
            raise ValueError('Duplicate ZIP members')
        manifests = [name for name in names if name.endswith('/RELEASE-MANIFEST.json')]
        if len(manifests) != 1:
            raise ValueError('Expected one release manifest')
        manifest = json.loads(bundle.read(manifests[0]))
        release_version = manifest['version']
        prefix = f'zeonexus-gateway-{release_version}/'
        records = manifest['files']
        paths = [item['path'] for item in records]
        if len(paths) != len(set(paths)) or not REQUIRED.issubset(paths):
            raise ValueError('Required or unique payload files missing')
        expected = {prefix + path for path in paths} | {prefix + 'RELEASE-MANIFEST.json'}
        if set(names) != expected:
            raise ValueError('ZIP members differ from manifest')
        for record in records:
            info = bundle.getinfo(prefix + record['path'])
            if stat.S_ISLNK(info.external_attr >> 16):
                raise ValueError('Symbolic links are not permitted')
            data = bundle.read(info)
            if len(data) != record['size'] or digest(data) != record['sha256']:
                raise ValueError('File checksum mismatch: ' + record['path'])
        return len(records)


def write_checksums(output):
    names = sorted(path.name for path in output.iterdir()
                   if path.is_file() and path.name != 'SHA256SUMS' and not path.name.startswith('.'))
    (output / 'SHA256SUMS').write_text(''.join(
        f'{digest((output / name).read_bytes())}  {name}\n' for name in names))


def build(output):
    release_version = version()
    released = date.today()
    payload = {}
    for path in sorted(ROOT.rglob('*')):
        if not path.is_file():
            continue
        name = path.relative_to(ROOT).as_posix()
        if not allowed(name):
            continue
        if path.is_symlink() or ROOT not in path.resolve().parents:
            raise ValueError('Refusing linked source path: ' + name)
        payload[name] = path.read_bytes()
    if not REQUIRED.issubset(payload):
        raise ValueError('Missing required files: ' + ', '.join(sorted(REQUIRED - payload.keys())))
    manifest = {
        'product': 'ZeoNexus Gateway',
        'version': release_version,
        'upstream_repository': 'https://github.com/zhangitit/one-api',
        'upstream_base_commit': '8df4a2670b98266bd287c698243fff327d9748cf',
        'distribution': 'source, deployment files and tests; dependencies resolved from go.sum',
        'files': [{'path': name, 'size': len(data), 'sha256': digest(data)} for name, data in payload.items()],
    }
    encoded_manifest = (json.dumps(manifest, ensure_ascii=False, indent=2) + '\n').encode()
    slug = f'zeonexus-gateway-{release_version}'
    output.mkdir(parents=True, exist_ok=True)
    archive_path = output / f'{slug}.zip'
    manifest_path = output / 'gateway-manifest.json'
    if archive_path.exists() or manifest_path.exists():
        raise ValueError('Gateway release output already exists')
    with tempfile.TemporaryDirectory(prefix='.gateway-packaging-', dir=output) as temporary:
        temporary_path = Path(temporary)
        archive = temporary_path / archive_path.name
        with zipfile.ZipFile(archive, 'w', compression=zipfile.ZIP_DEFLATED, compresslevel=6) as bundle:
            entries = dict(payload)
            entries['RELEASE-MANIFEST.json'] = encoded_manifest
            for name, data in entries.items():
                info = zipfile.ZipInfo(f'{slug}/{name}', (released.year, released.month, released.day, 0, 0, 0))
                info.compress_type = zipfile.ZIP_DEFLATED
                info.create_system = 3
                executable = name == 'scripts/package-release.py'
                info.external_attr = (stat.S_IFREG | (0o755 if executable else 0o644)) << 16
                bundle.writestr(info, data)
        count = verify(archive)
        archive.rename(archive_path)
        manifest_path.write_bytes(encoded_manifest)
    write_checksums(output)
    print(f'Created {archive_path} ({count} verified files)')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output-dir', type=Path)
    parser.add_argument('--verify', type=Path)
    args = parser.parse_args()
    try:
        if args.verify:
            print(f'PASS: {verify(args.verify)} files match the release manifest')
        else:
            build(args.output_dir or ROOT.parent / 'releases' / version())
    except (ValueError, OSError, KeyError, zipfile.BadZipFile) as error:
        parser.exit(1, 'Release check failed: ' + str(error) + '\n')
