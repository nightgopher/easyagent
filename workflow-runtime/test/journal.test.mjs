import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync,rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { openJournal } from '../src/journal.mjs';
test('journal survives process recreation',()=>{
 const dir=mkdtempSync(join(tmpdir(),'ea-journal-')),path=join(dir,'journal.sqlite');
 try {
  let journal=openJournal(path);journal.port.createRun({runId:'test',status:'running'});journal.port.putActor({runId:'test',siteId:'actor#1',ordinal:0,sessionId:'session-a'});journal.port.appendEvent('test',{type:'started'});const originalTime=journal.port.listEvents('test')[0].timeCreated;journal.port.putNode({runId:'test',siteId:'ask#1',ordinal:0,status:'completed',result:{answer:42}});journal.close();
  journal=openJournal(path);
  assert.equal(journal.port.listEvents('test')[0].timeCreated,originalTime);
  assert.equal(journal.port.getActor('test','actor#1',0).sessionId,'session-a');assert.equal(journal.port.getNode('test','ask#1',0).result.answer,42);
  journal.port.updateRunStatus('test','completed',{result:'done'});journal.close();
  journal=openJournal(path);assert.equal(journal.port.getRun('test').result,'done');journal.close();
 }finally{rmSync(dir,{recursive:true});}
});
