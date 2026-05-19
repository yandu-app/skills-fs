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
    assert.throws(() => fs.read('/tmp.txt'), /ENOENT|not found|read\(/);
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
    assert.throws(() => fs.read('/old.txt'), /ENOENT|not found|read\(/);
    assert.strictEqual(fs.read('/new.txt').toString(), 'payload');
    console.log('ok  rename moves data with path');
  } finally {
    fs.shutdown();
  }
}

function testStatReportsBlobSize() {
  const fs = new FileSystem();
  try {
    fs.mountBlob('/info.txt', 0o600);
    fs.write('/info.txt', Buffer.from('twelve chars'));

    const st = fs.stat('/info.txt');
    assert.strictEqual(st.path, '/info.txt');
    assert.strictEqual(st.kind, 'blob');
    assert.strictEqual(st.mode, 0o600);
    assert.strictEqual(st.size, 12);
    console.log('ok  stat reports kind, mode, size');
  } finally {
    fs.shutdown();
  }
}

function testReaddirListsBuiltinSysDir() {
  const fs = new FileSystem();
  try {
    const entries = fs.readdir('/sys');
    assert.ok(Array.isArray(entries), 'readdir must return Array');
    const names = entries.map((e) => e.name);
    assert.ok(names.includes('metrics'), `/sys should expose metrics, got ${names}`);
    console.log('ok  readdir lists /sys built-in entries');
  } finally {
    fs.shutdown();
  }
}

function testMountApiRejectsMissingProvider() {
  const fs = new FileSystem();
  try {
    // Mounting an API node without a registered provider must fail
    // with a real error message (not just rc=-1).
    assert.throws(
      () => fs.mountApi('/api/greet', 'greet-provider', 'sayHello'),
      /EINVAL|provider|not found/,
    );
    console.log('ok  mount_api rejects missing provider');
  } finally {
    fs.shutdown();
  }
}

function testErrorMessagesArePropagated() {
  const fs = new FileSystem();
  try {
    // Reading an unmounted path must surface the underlying core error
    // (typically containing "ENOENT" or "not found") rather than a
    // generic rc=-1.
    let err;
    try {
      fs.read('/does-not-exist.txt');
    } catch (e) {
      err = e;
    }
    assert.ok(err instanceof Error, 'expected Error to be thrown');
    assert.ok(
      err.message.length > 'read(/does-not-exist.txt): rc=-1'.length,
      `expected real error message, got: ${err.message}`,
    );
    assert.ok(
      !/rc=-1$/.test(err.message),
      `error fell back to rc=-1: ${err.message}`,
    );
    console.log(`ok  error messages propagated (${err.message})`);
  } finally {
    fs.shutdown();
  }
}

testBlobRoundTrip();
testShutdownIdempotent();
testUseAfterShutdownThrows();
testUnmountRemovesMount();
testRenameMovesData();
testStatReportsBlobSize();
testReaddirListsBuiltinSysDir();
testMountApiRejectsMissingProvider();
testErrorMessagesArePropagated();
console.log('all tests passed');
