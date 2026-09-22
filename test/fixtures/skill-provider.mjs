#!/usr/bin/env node
// Deterministic protocol fixture. Never imports a real client or contacts a model.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import readline from 'node:readline';

function probe(prompt) {
  assert.equal(process.getuid(), 10002);
  assert.equal(process.getgid(), 10002);
  assert.ok(process.getgroups().includes(10003));
  assert.match(fs.readFileSync('/proc/self/status', 'utf8'), /CapEff:\s+0000000000000000/);
  const instructions = prompt.match(/Read instructions: (\/skills\/[^\n]+\/SKILL\.md)/)?.[1];
  assert.ok(instructions, 'no shared package path in actual provider prompt');
  assert.match(fs.readFileSync(instructions, 'utf8'), /IMAGE-PRIVATE-INSTRUCTION/);
  const reference = path.join(path.dirname(instructions), 'references/example.txt');
  assert.equal(fs.readFileSync(reference, 'utf8'), 'versioned reference');
  assert.throws(() => fs.writeFileSync(reference, 'changed'), { code: 'EACCES' });
  assert.throws(() => fs.unlinkSync(reference), { code: 'EACCES' });
  assert.throws(() => fs.readFileSync('/runtime/skill-test-secret'), { code: 'EACCES' });
  return 'skill-files-readable-state-private';
}

const send = (value) => process.stdout.write(JSON.stringify(value) + '\n');
if (!process.argv.includes('app-server')) {
  let prompt = '';
  for await (const chunk of process.stdin) prompt += chunk;
  assert.ok(process.argv.includes('--add-dir'));
  send({ type: 'result', subtype: 'success', result: probe(prompt) });
} else {
  for await (const line of readline.createInterface({ input: process.stdin })) {
    const message = JSON.parse(line);
    const reply = (result) => send({ id: message.id, result });
    switch (message.method) {
      case 'initialize': reply({ userAgent: 'skill-image-fixture' }); break;
      case 'initialized': break;
      case 'thread/start':
      case 'thread/resume': reply({ thread: { id: '11111111-1111-4111-8111-111111111111' } }); break;
      case 'turn/start': {
        const text = probe(message.params.input[0].text);
        const threadId = message.params.threadId;
        const turn = { id: 'turn-skill', status: 'inProgress', items: [] };
        reply({ turn });
        send({ method: 'turn/started', params: { threadId, turn } });
        send({ method: 'item/completed', params: { threadId, turnId: turn.id, item: { id: 'answer', type: 'agentMessage', phase: 'final_answer', text } } });
        send({ method: 'turn/completed', params: { threadId, turn: { ...turn, status: 'completed' } } });
        break;
      }
      default: throw new Error(`unexpected method ${message.method}`);
    }
  }
}
