export namespace backup {
	
	export class DriveAccount {
	    myDrive: string;
	    label: string;
	
	    static createFrom(source: any = {}) {
	        return new DriveAccount(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.myDrive = source["myDrive"];
	        this.label = source["label"];
	    }
	}
	export class DriveInfo {
	    found: boolean;
	    running: boolean;
	    myDrive: string;
	    drives: DriveAccount[];
	
	    static createFrom(source: any = {}) {
	        return new DriveInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.found = source["found"];
	        this.running = source["running"];
	        this.myDrive = source["myDrive"];
	        this.drives = this.convertValues(source["drives"], DriveAccount);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Orphan {
	    id: string;
	    label: string;
	    path: string;
	    host: string;
	    mine: boolean;
	    // Go type: time
	    backedUp: any;
	    // Go type: time
	    modified: any;
	    bytes: number;
	    files: number;
	    points: number;
	
	    static createFrom(source: any = {}) {
	        return new Orphan(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.label = source["label"];
	        this.path = source["path"];
	        this.host = source["host"];
	        this.mine = source["mine"];
	        this.backedUp = this.convertValues(source["backedUp"], null);
	        this.modified = this.convertValues(source["modified"], null);
	        this.bytes = source["bytes"];
	        this.files = source["files"];
	        this.points = source["points"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace conflict {
	
	export class Conflict {
	    rel: string;
	    copy: string;
	    device: string;
	    deviceName: string;
	    size: number;
	    // Go type: time
	    modified: any;
	    missing: boolean;
	    copySize: number;
	    // Go type: time
	    copyModified: any;
	
	    static createFrom(source: any = {}) {
	        return new Conflict(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rel = source["rel"];
	        this.copy = source["copy"];
	        this.device = source["device"];
	        this.deviceName = source["deviceName"];
	        this.size = source["size"];
	        this.modified = this.convertValues(source["modified"], null);
	        this.missing = source["missing"];
	        this.copySize = source["copySize"];
	        this.copyModified = this.convertValues(source["copyModified"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace main {
	
	export class AvailableView {
	    id: string;
	    label: string;
	    path: string;
	    from: string;
	    reason: string;
	
	    static createFrom(source: any = {}) {
	        return new AvailableView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.label = source["label"];
	        this.path = source["path"];
	        this.from = source["from"];
	        this.reason = source["reason"];
	    }
	}
	export class BulkResult {
	    added: string[];
	    skipped: string[];
	
	    static createFrom(source: any = {}) {
	        return new BulkResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.added = source["added"];
	        this.skipped = source["skipped"];
	    }
	}
	export class DeviceView {
	    id: string;
	    name: string;
	    connected: boolean;
	    address: string;
	    completion: number;
	    needBytes: number;
	
	    static createFrom(source: any = {}) {
	        return new DeviceView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.connected = source["connected"];
	        this.address = source["address"];
	        this.completion = source["completion"];
	        this.needBytes = source["needBytes"];
	    }
	}
	export class PendingView {
	    id: string;
	    name: string;
	    address: string;
	    // Go type: time
	    time: any;
	
	    static createFrom(source: any = {}) {
	        return new PendingView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.address = source["address"];
	        this.time = this.convertValues(source["time"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class DevicesView {
	    myID: string;
	    myName: string;
	    qr: string;
	    devices: DeviceView[];
	    pending: PendingView[];
	
	    static createFrom(source: any = {}) {
	        return new DevicesView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.myID = source["myID"];
	        this.myName = source["myName"];
	        this.qr = source["qr"];
	        this.devices = this.convertValues(source["devices"], DeviceView);
	        this.pending = this.convertValues(source["pending"], PendingView);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class FolderView {
	    id: string;
	    label: string;
	    path: string;
	    state: string;
	    bytes: number;
	    files: number;
	    needBytes: number;
	    errors: number;
	    backup: boolean;
	    sync: boolean;
	    installed: boolean;
	    exists: boolean;
	    shared: number;
	    conflicts: number;
	    // Go type: time
	    modified: any;
	    // Go type: time
	    backedUp: any;
	    backupBytes: number;
	    points: number;
	    exclude: string[];
	    newerOn: string;
	    // Go type: time
	    newerAt: any;
	    inside: string;
	    oneDrive: boolean;
	
	    static createFrom(source: any = {}) {
	        return new FolderView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.label = source["label"];
	        this.path = source["path"];
	        this.state = source["state"];
	        this.bytes = source["bytes"];
	        this.files = source["files"];
	        this.needBytes = source["needBytes"];
	        this.errors = source["errors"];
	        this.backup = source["backup"];
	        this.sync = source["sync"];
	        this.installed = source["installed"];
	        this.exists = source["exists"];
	        this.shared = source["shared"];
	        this.conflicts = source["conflicts"];
	        this.modified = this.convertValues(source["modified"], null);
	        this.backedUp = this.convertValues(source["backedUp"], null);
	        this.backupBytes = source["backupBytes"];
	        this.points = source["points"];
	        this.exclude = source["exclude"];
	        this.newerOn = source["newerOn"];
	        this.newerAt = this.convertValues(source["newerAt"], null);
	        this.inside = source["inside"];
	        this.oneDrive = source["oneDrive"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class GameView {
	    name: string;
	    path: string;
	    steamCloud: boolean;
	    steamCloudUnverified: boolean;
	    steamCloudReason: string;
	    emulator: string;
	    oneDrive: boolean;
	    known: boolean;
	    size: number;
	    files: number;
	    // Go type: time
	    modified: any;
	    syncedBy: string;
	    installed: boolean;
	
	    static createFrom(source: any = {}) {
	        return new GameView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.steamCloud = source["steamCloud"];
	        this.steamCloudUnverified = source["steamCloudUnverified"];
	        this.steamCloudReason = source["steamCloudReason"];
	        this.emulator = source["emulator"];
	        this.oneDrive = source["oneDrive"];
	        this.known = source["known"];
	        this.size = source["size"];
	        this.files = source["files"];
	        this.modified = this.convertValues(source["modified"], null);
	        this.syncedBy = source["syncedBy"];
	        this.installed = source["installed"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class NewFolder {
	    label: string;
	    path: string;
	
	    static createFrom(source: any = {}) {
	        return new NewFolder(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.label = source["label"];
	        this.path = source["path"];
	    }
	}
	export class UpdateInfo {
	    latest: string;
	    url: string;
	
	    static createFrom(source: any = {}) {
	        return new UpdateInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.latest = source["latest"];
	        this.url = source["url"];
	    }
	}
	export class SyncthingInfo {
	    installed: boolean;
	    running: boolean;
	    myID: string;
	    gui: string;
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new SyncthingInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.installed = source["installed"];
	        this.running = source["running"];
	        this.myID = source["myID"];
	        this.gui = source["gui"];
	        this.error = source["error"];
	    }
	}
	export class Overview {
	    syncthing: SyncthingInfo;
	    devices: number;
	    online: number;
	    pending: number;
	    folders: number;
	    syncing: number;
	    errors: number;
	    conflicts: number;
	    overlaps: number;
	    drive: backup.DriveInfo;
	    target: string;
	    lastBackup?: store.BackupRun;
	    backingUp: boolean;
	    gaming: boolean;
	    paused: boolean;
	    settings: store.Settings;
	    version: string;
	    update?: UpdateInfo;
	
	    static createFrom(source: any = {}) {
	        return new Overview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.syncthing = this.convertValues(source["syncthing"], SyncthingInfo);
	        this.devices = source["devices"];
	        this.online = source["online"];
	        this.pending = source["pending"];
	        this.folders = source["folders"];
	        this.syncing = source["syncing"];
	        this.errors = source["errors"];
	        this.conflicts = source["conflicts"];
	        this.overlaps = source["overlaps"];
	        this.drive = this.convertValues(source["drive"], backup.DriveInfo);
	        this.target = source["target"];
	        this.lastBackup = this.convertValues(source["lastBackup"], store.BackupRun);
	        this.backingUp = source["backingUp"];
	        this.gaming = source["gaming"];
	        this.paused = source["paused"];
	        this.settings = this.convertValues(source["settings"], store.Settings);
	        this.version = source["version"];
	        this.update = this.convertValues(source["update"], UpdateInfo);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	export class UndoOptions {
	    unpair: boolean;
	    stopBackups: boolean;
	    deleteBackups: boolean;
	    stopSyncthing: boolean;
	    uninstallSyncthing: boolean;
	
	    static createFrom(source: any = {}) {
	        return new UndoOptions(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.unpair = source["unpair"];
	        this.stopBackups = source["stopBackups"];
	        this.deleteBackups = source["deleteBackups"];
	        this.stopSyncthing = source["stopSyncthing"];
	        this.uninstallSyncthing = source["uninstallSyncthing"];
	    }
	}
	export class UndoReport {
	    folders: number;
	    devices: number;
	    notes: string[];
	
	    static createFrom(source: any = {}) {
	        return new UndoReport(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.folders = source["folders"];
	        this.devices = source["devices"];
	        this.notes = source["notes"];
	    }
	}

}

export namespace store {
	
	export class BackupRun {
	    // Go type: time
	    started: any;
	    // Go type: time
	    finished: any;
	    ok: boolean;
	    folders: number;
	    copied: number;
	    versions: number;
	    bytes: number;
	    errors: string[];
	    target: string;
	
	    static createFrom(source: any = {}) {
	        return new BackupRun(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.started = this.convertValues(source["started"], null);
	        this.finished = this.convertValues(source["finished"], null);
	        this.ok = source["ok"];
	        this.folders = source["folders"];
	        this.copied = source["copied"];
	        this.versions = source["versions"];
	        this.bytes = source["bytes"];
	        this.errors = source["errors"];
	        this.target = source["target"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LocalFolder {
	    id: string;
	    label: string;
	    path: string;
	    syncID?: string;
	    copiedFrom?: string;
	
	    static createFrom(source: any = {}) {
	        return new LocalFolder(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.label = source["label"];
	        this.path = source["path"];
	        this.syncID = source["syncID"];
	        this.copiedFrom = source["copiedFrom"];
	    }
	}
	export class Settings {
	    theme: string;
	    backupEnabled: boolean;
	    backupRoot: string;
	    driveRoot: string;
	    intervalHours: number;
	    keepDays: number;
	    noBackup: Record<string, boolean>;
	    ignored: Record<string, boolean>;
	    showSteamCloud: boolean;
	    autoAdd: boolean;
	    autoAddMaxGB: number;
	    dismissed: Record<string, boolean>;
	    closeToTray: boolean;
	    startAtLogin: boolean;
	    migrated: boolean;
	    pauseWhileGaming: boolean;
	    installedOnly: boolean;
	    syncDisabled: boolean;
	    backupOnly: Record<string, LocalFolder>;
	    // Go type: time
	    pausedUntil: any;
	    notify: boolean;
	    noUpdateCheck: boolean;
	    exclude?: Record<string, Array<string>>;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.theme = source["theme"];
	        this.backupEnabled = source["backupEnabled"];
	        this.backupRoot = source["backupRoot"];
	        this.driveRoot = source["driveRoot"];
	        this.intervalHours = source["intervalHours"];
	        this.keepDays = source["keepDays"];
	        this.noBackup = source["noBackup"];
	        this.ignored = source["ignored"];
	        this.showSteamCloud = source["showSteamCloud"];
	        this.autoAdd = source["autoAdd"];
	        this.autoAddMaxGB = source["autoAddMaxGB"];
	        this.dismissed = source["dismissed"];
	        this.closeToTray = source["closeToTray"];
	        this.startAtLogin = source["startAtLogin"];
	        this.migrated = source["migrated"];
	        this.pauseWhileGaming = source["pauseWhileGaming"];
	        this.installedOnly = source["installedOnly"];
	        this.syncDisabled = source["syncDisabled"];
	        this.backupOnly = this.convertValues(source["backupOnly"], LocalFolder, true);
	        this.pausedUntil = this.convertValues(source["pausedUntil"], null);
	        this.notify = source["notify"];
	        this.noUpdateCheck = source["noUpdateCheck"];
	        this.exclude = source["exclude"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

