import { spawnSync } from 'node:child_process';

const image = 'apophis-teamseatwatch:ticket-01-verify';
const artifact = spawnSync('python3', ['tools/prepare-browser.py'], { stdio: 'inherit' });
if (artifact.error || artifact.status !== 0) process.exit(artifact.status ?? 1);
const build = spawnSync('docker', ['build', '--pull=false', '--platform', 'linux/amd64', '-t', image, '.'], { stdio: 'inherit' });
if (build.error || build.status !== 0) {
  process.exit(build.status ?? 1);
}
const smoke = spawnSync('docker', ['run', '--rm', image, 'migrate'], { encoding: 'utf8' });
const output = `${smoke.stdout ?? ''}${smoke.stderr ?? ''}`;
if (smoke.status === 0 || !output.includes('"code":"config_missing"')) {
  process.stderr.write(output);
  process.exit(1);
}
console.log(`OCI smoke passed: ${image}`);
