export namespace backup {
	
	export class DriveInfo {
	    found: boolean;
	    running: boolean;
	    myDrive: string;
	
	    static createFrom(source: any = {}) {
	        return new DriveInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.found = source["found"];
	        this.running = source["running"];
	        this.myDrive = source["myDrive"];
	    }
	}

}

export namespace main {
	
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
	    exists: boolean;
	    shared: number;
	    // Go type: time
	    modified: any;
	
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
	        this.exists = source["exists"];
	        this.shared = source["shared"];
	        this.modified = this.convertValues(source["modified"], null);
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
	    known: boolean;
	    size: number;
	    files: number;
	    // Go type: time
	    modified: any;
	    syncedBy: string;
	
	    static createFrom(source: any = {}) {
	        return new GameView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.steamCloud = source["steamCloud"];
	        this.known = source["known"];
	        this.size = source["size"];
	        this.files = source["files"];
	        this.modified = this.convertValues(source["modified"], null);
	        this.syncedBy = source["syncedBy"];
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
	    drive: backup.DriveInfo;
	    target: string;
	    lastBackup?: store.BackupRun;
	    backingUp: boolean;
	    settings: store.Settings;
	
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
	        this.drive = this.convertValues(source["drive"], backup.DriveInfo);
	        this.target = source["target"];
	        this.lastBackup = this.convertValues(source["lastBackup"], store.BackupRun);
	        this.backingUp = source["backingUp"];
	        this.settings = this.convertValues(source["settings"], store.Settings);
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
	export class Settings {
	    theme: string;
	    backupEnabled: boolean;
	    backupRoot: string;
	    intervalHours: number;
	    keepDays: number;
	    noBackup: Record<string, boolean>;
	    ignored: Record<string, boolean>;
	    showSteamCloud: boolean;
	    migrated: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.theme = source["theme"];
	        this.backupEnabled = source["backupEnabled"];
	        this.backupRoot = source["backupRoot"];
	        this.intervalHours = source["intervalHours"];
	        this.keepDays = source["keepDays"];
	        this.noBackup = source["noBackup"];
	        this.ignored = source["ignored"];
	        this.showSteamCloud = source["showSteamCloud"];
	        this.migrated = source["migrated"];
	    }
	}

}

