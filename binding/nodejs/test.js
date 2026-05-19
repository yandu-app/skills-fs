'use strict';

const assert = require('node:assert');
const { FileSystem } = require('./index');

function testBlobRoundTrip() {
  const fs = new FileSystem();
  try {
    fs.mountBlob('/hello.txt', 0o644);
    fs.write('/hello.txt', Buffer.from('hello from node'));
    const got = fs.read('/hello.txt');
    assert.ok(Buffer.isBuffer(got), 'read() must return Buffer');
    assert.strictEqual(got.toString(), 'hello from node');
    console.log('ok  blob round-trip');
  } finally {
    fs.shutdown();
  }
}

function testShutdownIdempotent() {
  const fs = new FileSystem();
  fs.shutdown();
  fs.shutdown();
  console.log('ok  shutdown is idempotent');
}

function testUseAfterShutdownThrows() {
  const fs = new FileSystem();
  fs.shutdown();
  assert.throws(() => fs.read('/x'), /shut down/);
  console.log('ok  use-after-shutdown throws');
}

testBlobRoundTrip();
testShutdownIdempotent();
testUseAfterShutdownThrows();
console.log('all tests passed');
