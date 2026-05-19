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

function testUnmountRemovesMount() {
  const fs = new FileSystem();
  try {
    fs.mountBlob('/tmp.txt', 0o644);
    fs.write('/tmp.txt', Buffer.from('temp'));
    assert.strictEqual(fs.read('/tmp.txt').toString(), 'temp');

    fs.unmount('/tmp.txt');
    assert.throws(() => fs.read('/tmp.txt'), /read.*failed/);
    console.log('ok  unmount removes mount');
  } finally {
    fs.shutdown();
  }
}

function testRenameMovesData() {
  const fs = new FileSystem();
  try {
    fs.mountBlob('/old.txt', 0o644);
    fs.write('/old.txt', Buffer.from('payload'));

    fs.rename('/old.txt', '/new.txt');
    assert.throws(() => fs.read('/old.txt'), /read.*failed/);
    assert.strictEqual(fs.read('/new.txt').toString(), 'payload');
    console.log('ok  rename moves data with path');
  } finally {
    fs.shutdown();
  }
}

testBlobRoundTrip();
testShutdownIdempotent();
testUseAfterShutdownThrows();
testUnmountRemovesMount();
testRenameMovesData();
console.log('all tests passed');
