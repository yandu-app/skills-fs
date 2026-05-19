'use strict';

const addon = require('./build/Release/skills_fs.node');

class FileSystem {
  constructor() {
    this._handle = addon.createFS();
    this._closed = false;
  }

  mountBlob(path, mode = 0o644) {
    this._assertOpen();
    const rc = addon.mountBlob(this._handle, path, mode >>> 0);
    if (rc !== 0) {
      throw new Error(`mountBlob(${path}) failed: rc=${rc}`);
    }
  }

  read(path) {
    this._assertOpen();
    const buf = addon.read(this._handle, path);
    if (buf === undefined) {
      throw new Error(`read(${path}) failed`);
    }
    return buf;
  }

  write(path, data) {
    this._assertOpen();
    const buf = Buffer.isBuffer(data) ? data : Buffer.from(data);
    const rc = addon.write(this._handle, path, buf);
    if (rc !== 0) {
      throw new Error(`write(${path}) failed: rc=${rc}`);
    }
  }

  shutdown() {
    if (this._closed) return;
    addon.shutdown(this._handle);
    this._closed = true;
  }

  _assertOpen() {
    if (this._closed) {
      throw new Error('FileSystem has been shut down');
    }
  }
}

module.exports = { FileSystem };
