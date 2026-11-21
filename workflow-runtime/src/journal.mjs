import { InMemoryJournalStore } from '@zcode/dynamic-workflow';
import { DatabaseSync } from 'node:sqlite';

// Commit each engine mutation before acknowledging it to the runtime.
export function openJournal(path) {
  const base = new InMemoryJournalStore();
  const timestamps = new Map();
  const db = new DatabaseSync(path);
  db.exec('PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; CREATE TABLE IF NOT EXISTS operations (seq INTEGER PRIMARY KEY, data TEXT NOT NULL)');
  const insert = db.prepare('INSERT INTO operations(data) VALUES (?)');
  try {
    for (const row of db.prepare('SELECT data FROM operations ORDER BY seq').iterate()) {
      const { method, args, timeCreated } = JSON.parse(row.data);
      if (!writes.has(method)) throw Error('invalid journal operation');
      const value = base[method](...args);
      if (method === 'appendEvent') timestamps.set(`${args[0]}:${value.sequence}`, timeCreated ?? value.timeCreated);
    }
  } catch (err) { db.close(); throw err; }
  const port = new Proxy(base, { get(target, key) {
    const fn = target[key];
    if (typeof fn !== 'function') return fn;
    return (...args) => {
      const value = fn.apply(target, args);
      if (writes.has(key)) {
        const timeCreated = key === 'appendEvent' ? value.timeCreated : undefined;
        insert.run(JSON.stringify({ method: key, args, timeCreated }));
        if (key === 'appendEvent') timestamps.set(`${args[0]}:${value.sequence}`, timeCreated);
      }
      if (key === 'listEvents') return value.map(event => ({
        ...event, timeCreated: timestamps.get(`${args[0]}:${event.sequence}`) ?? event.timeCreated,
      }));
      return value;
    };
  } });
  return { port, close: () => db.close() };
}
const writes = new Set(['createRun', 'updateRunStatus', 'updateRunUsage', 'updateRunCaps', 'putActor', 'putNode', 'appendEvent']);
