'use strict';

const addon = require('./build/Release/skills_fs.node');

class FileSystem {
  constructor() {
    this._handle = addon.createFS();
    this._closed = false;
  }

  mountBlob(path, mode = 0o644) {
    this._assertOpen();
    this._check(
      addon.mountBlob(this._handle, path, mode >>> 0),
      `mountBlob(${path})`,
    );
  }

  unmount(path) {
    this._assertOpen();
    this._check(addon.unmount(this._handle, path), `unmount(${path})`);
  }

  rename(oldPath, newPath) {
    this._assertOpen();
    this._check(
      addon.rename(this._handle, oldPath, newPath),
      `rename(${oldPath} -> ${newPath})`,
    );
  }

  read(path) {
    this._assertOpen();
    const buf = addon.read(this._handle, path);
    if (buf === undefined) {
      throw this._error(`read(${path})`);
    }
    return buf;
  }

  write(path, data) {
    this._assertOpen();
    const buf = Buffer.isBuffer(data) ? data : Buffer.from(data);
    this._check(addon.write(this._handle, path, buf), `write(${path})`);
  }

  stat(path) {
    this._assertOpen();
    const json = addon.stat(this._handle, path);
    if (json === undefined) {
      throw this._error(`stat(${path})`);
    }
    return JSON.parse(json);
  }

  readdir(path) {
    this._assertOpen();
    const json = addon.readdir(this._handle, path);
    if (json === undefined) {
      throw this._error(`readdir(${path})`);
    }
    return JSON.parse(json);
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

  _check(rc, op) {
    if (rc === 0) return;
    throw this._error(op, rc);
  }

  _error(op, rc) {
    const msg = addon.lastError(this._handle);
    const detail = msg || (rc !== undefined ? `rc=${rc}` : 'failed');
    return new Error(`${op}: ${detail}`);
  }
}

module.exports = { FileSystem };
