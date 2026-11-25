// Builds the app and packs it into claudemux-glasses.ehpk.
//
//   CLAUDEMUX_REMOTE_ORIGIN=http://100.64.0.15:7474 node scripts/pack.mjs
//
// The lobby origin is the only network host the package may reach and cannot
// change after packing, so it is baked in here. The token is never read or
// written by this script: it is entered on the phone after install.
import { spawnSync } from 'node:child_process';
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const origin = process.env.CLAUDEMUX_REMOTE_ORIGIN ?? '';

// http(s)://host[:port], nothing else: no path, query, credentials or spaces.
const ORIGIN_RE = /^https?:\/\/[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:\d{1,5})?$/;

if (origin === '') {
  console.error('CLAUDEMUX_REMOTE_ORIGIN is not set. Set it to the lobby origin, for example:');
  console.error('  CLAUDEMUX_REMOTE_ORIGIN=http://100.64.0.15:7474 node scripts/pack.mjs');
  process.exit(1);
}
if (!ORIGIN_RE.test(origin)) {
  console.error(`CLAUDEMUX_REMOTE_ORIGIN must look like http(s)://host[:port] (no path or trailing slash), got: ${origin}`);
  process.exit(1);
}

const manifest = readFileSync(join(root, 'app.json.tmpl'), 'utf8').replace('{{ORIGIN}}', origin);
JSON.parse(manifest); // fail early if the template is broken
writeFileSync(join(root, 'app.json'), manifest);

function run(cmd, args) {
  const r = spawnSync(cmd, args, { cwd: root, stdio: 'inherit', env: process.env });
  if (r.status !== 0) {
    console.error(`${cmd} ${args.join(' ')} failed`);
    process.exit(r.status ?? 1);
  }
}

run('npx', ['vite', 'build']);
run('npx', ['evenhub', 'pack', 'app.json', 'dist', '-o', 'claudemux-glasses.ehpk']);
console.log(`packed claudemux-glasses.ehpk for ${origin}`);
