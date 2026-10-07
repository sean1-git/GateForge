// Precompress public assets to save request-time CPU without introducing
// compression side channels around administrator API secrets.
import { readdir, readFile, writeFile } from 'node:fs/promises';
import { gzipSync } from 'node:zlib';
const dir = new URL('./dist/assets/', import.meta.url);
for (const name of await readdir(dir)) {
  if (!/\.(js|css)$/.test(name)) continue;
  const data = await readFile(new URL(name, dir));
  await writeFile(new URL(`${name}.gz`, dir), gzipSync(data, { level: 9 }));
}
