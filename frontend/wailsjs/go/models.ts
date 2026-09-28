export namespace accounts {
	
	export class Account {
	    id: string;
	    name: string;
	    color?: string;
	    // Go type: time
	    created: any;
	    // Go type: time
	    updated: any;
	    deleted?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Account(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.color = source["color"];
	        this.created = this.convertValues(source["created"], null);
	        this.updated = this.convertValues(source["updated"], null);
	        this.deleted = source["deleted"];
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
	export class Origin {
	    by?: string[];
	    from?: string[];
	
	    static createFrom(source: any = {}) {
	        return new Origin(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.by = source["by"];
	        this.from = source["from"];
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
	    currentName: string;
	
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
	        this.currentName = source["currentName"];
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
	
	export class SaveFile {
	    rel: string;
	    size: number;
	    // Go type: time
	    modified: any;
	    owner?: string;
	
	    static createFrom(source: any = {}) {
	        return new SaveFile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rel = source["rel"];
	        this.size = source["size"];
	        this.modified = this.convertValues(source["modified"], null);
	        this.owner = source["owner"];
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
	export class SplitSave {
	    game: string;
	    folderID: string;
	    label: string;
	    path: string;
	    here: boolean;
	    synced: boolean;
	    files: SaveFile[];
	    more: number;
	    bytes: number;
	    conflicts: number;
	    // Go type: time
	    modified: any;
	
	    static createFrom(source: any = {}) {
	        return new SplitSave(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.game = source["game"];
	        this.folderID = source["folderID"];
	        this.label = source["label"];
	        this.path = source["path"];
	        this.here = source["here"];
	        this.synced = source["synced"];
	        this.files = this.convertValues(source["files"], SaveFile);
	        this.more = source["more"];
	        this.bytes = source["bytes"];
	        this.conflicts = source["conflicts"];
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
	export class AccountView {
	    id: string;
	    name: string;
	    color?: string;
	    // Go type: time
	    created: any;
	    // Go type: time
	    updated: any;
	    deleted?: boolean;
	    active: boolean;
	    games: SplitSave[];
	    pcs: string[];
	
	    static createFrom(source: any = {}) {
	        return new AccountView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.color = source["color"];
	        this.created = this.convertValues(source["created"], null);
	        this.updated = this.convertValues(source["updated"], null);
	        this.deleted = source["deleted"];
	        this.active = source["active"];
	        this.games = this.convertValues(source["games"], SplitSave);
	        this.pcs = source["pcs"];
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
	export class PendingChange {
	    game: string;
	    label: string;
	    kind: string;
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new PendingChange(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.game = source["game"];
	        this.label = source["label"];
	        this.kind = source["kind"];
	        this.error = source["error"];
	    }
	}
	export class SplitView {
	    game: string;
	    label: string;
	    accounts: string[];
	    here: string;
	
	    static createFrom(source: any = {}) {
	        return new SplitView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.game = source["game"];
	        this.label = source["label"];
	        this.accounts = source["accounts"];
	        this.here = source["here"];
	    }
	}
	export class AccountsView {
	    enabled: boolean;
	    active: string;
	    accounts: AccountView[];
	    splits: SplitView[];
	    waiting: string[];
	    op: string;
	    opLabel: string;
	    opError: string;
	    pending: PendingChange[];
	
	    static createFrom(source: any = {}) {
	        return new AccountsView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.active = source["active"];
	        this.accounts = this.convertValues(source["accounts"], AccountView);
	        this.splits = this.convertValues(source["splits"], SplitView);
	        this.waiting = source["waiting"];
	        this.op = source["op"];
	        this.opLabel = source["opLabel"];
	        this.opError = source["opError"];
	        this.pending = this.convertValues(source["pending"], PendingChange);
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
	export class ApplyConfirm {
	    deletes: boolean;
	    gameFiles: boolean;
	    code: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ApplyConfirm(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.deletes = source["deletes"];
	        this.gameFiles = source["gameFiles"];
	        this.code = source["code"];
	    }
	}
	export class AvailableView {
	    id: string;
	    label: string;
	    path: string;
	    from: string;
	    reason: string;
	    kind: string;
	    modGame: string;
	
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
	        this.kind = source["kind"];
	        this.modGame = source["modGame"];
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
	export class ConflictOwner {
	    copy: string;
	    current: string;
	    other: string;
	
	    static createFrom(source: any = {}) {
	        return new ConflictOwner(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.copy = source["copy"];
	        this.current = source["current"];
	        this.other = source["other"];
	    }
	}
	export class DecisionView {
	    at: number;
	    rel: string;
	    kept: string;
	    other: string;
	
	    static createFrom(source: any = {}) {
	        return new DecisionView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.at = source["at"];
	        this.rel = source["rel"];
	        this.kept = source["kept"];
	        this.other = source["other"];
	    }
	}
	export class DeviceView {
	    id: string;
	    name: string;
	    connected: boolean;
	    address: string;
	    via: string;
	    completion: number;
	    needBytes: number;
	    version: string;
	    versionGap: string;
	
	    static createFrom(source: any = {}) {
	        return new DeviceView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.connected = source["connected"];
	        this.address = source["address"];
	        this.via = source["via"];
	        this.completion = source["completion"];
	        this.needBytes = source["needBytes"];
	        this.version = source["version"];
	        this.versionGap = source["versionGap"];
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
	    newerCanGet: boolean;
	    newerWhy: string;
	    inside: string;
	    oneDrive: boolean;
	    steamCloud: boolean;
	    ubisoftCloud: boolean;
	    copyOf: string;
	    oneDriveCopy: string;
	    oneDriveCopyNewer: boolean;
	    kind: string;
	    modGame: string;
	    modRole: string;
	    modPhase: string;
	    modHeld: string;
	    modHeldBy: string;
	    modPending: string;
	    split: boolean;
	    account: string;
	    game: string;
	
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
	        this.newerCanGet = source["newerCanGet"];
	        this.newerWhy = source["newerWhy"];
	        this.inside = source["inside"];
	        this.oneDrive = source["oneDrive"];
	        this.steamCloud = source["steamCloud"];
	        this.ubisoftCloud = source["ubisoftCloud"];
	        this.copyOf = source["copyOf"];
	        this.oneDriveCopy = source["oneDriveCopy"];
	        this.oneDriveCopyNewer = source["oneDriveCopyNewer"];
	        this.kind = source["kind"];
	        this.modGame = source["modGame"];
	        this.modRole = source["modRole"];
	        this.modPhase = source["modPhase"];
	        this.modHeld = source["modHeld"];
	        this.modHeldBy = source["modHeldBy"];
	        this.modPending = source["modPending"];
	        this.split = source["split"];
	        this.account = source["account"];
	        this.game = source["game"];
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
	    copyOf: string;
	    steamId: number;
	    oneDrive: boolean;
	    ubisoftCloud: boolean;
	    oneDriveCopy: string;
	    oneDriveCopyNewer: boolean;
	    known: boolean;
	    size: number;
	    files: number;
	    // Go type: time
	    modified: any;
	    kind?: string;
	    manager?: string;
	    modGame?: string;
	    modKey?: string;
	    warn?: string[];
	    syncedBy: string;
	    installed: boolean;
	    dismissed: boolean;
	
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
	        this.copyOf = source["copyOf"];
	        this.steamId = source["steamId"];
	        this.oneDrive = source["oneDrive"];
	        this.ubisoftCloud = source["ubisoftCloud"];
	        this.oneDriveCopy = source["oneDriveCopy"];
	        this.oneDriveCopyNewer = source["oneDriveCopyNewer"];
	        this.known = source["known"];
	        this.size = source["size"];
	        this.files = source["files"];
	        this.modified = this.convertValues(source["modified"], null);
	        this.kind = source["kind"];
	        this.manager = source["manager"];
	        this.modGame = source["modGame"];
	        this.modKey = source["modKey"];
	        this.warn = source["warn"];
	        this.syncedBy = source["syncedBy"];
	        this.installed = source["installed"];
	        this.dismissed = source["dismissed"];
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
	export class GoogleView {
	    available: boolean;
	    signedIn: boolean;
	    account: string;
	    // Go type: time
	    synced: any;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new GoogleView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.signedIn = source["signedIn"];
	        this.account = source["account"];
	        this.synced = this.convertValues(source["synced"], null);
	        this.error = source["error"];
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
	export class ModPreview {
	    id: string;
	    label: string;
	    from: string;
	    // Go type: time
	    updated: any;
	    added: number;
	    changed: number;
	    removed: number;
	    same: number;
	    bytes: number;
	    removedPlugins: string[];
	    pluginLists: string[];
	    needDeletes: boolean;
	    gameFiles: string[];
	    code: string[];
	    checks: mods.Check[];
	    ready: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ModPreview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.label = source["label"];
	        this.from = source["from"];
	        this.updated = this.convertValues(source["updated"], null);
	        this.added = source["added"];
	        this.changed = source["changed"];
	        this.removed = source["removed"];
	        this.same = source["same"];
	        this.bytes = source["bytes"];
	        this.removedPlugins = source["removedPlugins"];
	        this.pluginLists = source["pluginLists"];
	        this.needDeletes = source["needDeletes"];
	        this.gameFiles = source["gameFiles"];
	        this.code = source["code"];
	        this.checks = this.convertValues(source["checks"], mods.Check);
	        this.ready = source["ready"];
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
	export class VersionGap {
	    device: string;
	    name: string;
	    version: string;
	    gap: string;
	
	    static createFrom(source: any = {}) {
	        return new VersionGap(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.device = source["device"];
	        this.name = source["name"];
	        this.version = source["version"];
	        this.gap = source["gap"];
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
	    google: GoogleView;
	    target: string;
	    lastBackup?: store.BackupRun;
	    backingUp: boolean;
	    gaming: boolean;
	    paused: boolean;
	    settings: store.Settings;
	    version: string;
	    update?: UpdateInfo;
	    versionGaps: VersionGap[];
	
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
	        this.google = this.convertValues(source["google"], GoogleView);
	        this.target = source["target"];
	        this.lastBackup = this.convertValues(source["lastBackup"], store.BackupRun);
	        this.backingUp = source["backingUp"];
	        this.gaming = source["gaming"];
	        this.paused = source["paused"];
	        this.settings = this.convertValues(source["settings"], store.Settings);
	        this.version = source["version"];
	        this.update = this.convertValues(source["update"], UpdateInfo);
	        this.versionGaps = this.convertValues(source["versionGaps"], VersionGap);
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
	
	
	export class RestorePoint {
	    at: number;
	    by?: string[];
	    from?: string[];
	
	    static createFrom(source: any = {}) {
	        return new RestorePoint(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.at = source["at"];
	        this.by = source["by"];
	        this.from = source["from"];
	    }
	}
	export class RestorePointsView {
	    latest: backup.Origin;
	    points: RestorePoint[];
	
	    static createFrom(source: any = {}) {
	        return new RestorePointsView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.latest = this.convertValues(source["latest"], backup.Origin);
	        this.points = this.convertValues(source["points"], RestorePoint);
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
	
	export class SnapshotView {
	    stamp: string;
	    // Go type: time
	    created: any;
	    files: number;
	    added: number;
	    bytes: number;
	
	    static createFrom(source: any = {}) {
	        return new SnapshotView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.stamp = source["stamp"];
	        this.created = this.convertValues(source["created"], null);
	        this.files = source["files"];
	        this.added = source["added"];
	        this.bytes = source["bytes"];
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
	
	
	export class VortexShareView {
	    game: string;
	    name: string;
	    mods: number;
	    enabled: number;
	    waiting: number;
	    // Go type: time
	    checked: any;
	    // Go type: time
	    applied: any;
	    appliedN: number;
	    err: string;
	    peers: string[];
	    pushing: boolean;
	
	    static createFrom(source: any = {}) {
	        return new VortexShareView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.game = source["game"];
	        this.name = source["name"];
	        this.mods = source["mods"];
	        this.enabled = source["enabled"];
	        this.waiting = source["waiting"];
	        this.checked = this.convertValues(source["checked"], null);
	        this.applied = this.convertValues(source["applied"], null);
	        this.appliedN = source["appliedN"];
	        this.err = source["err"];
	        this.peers = source["peers"];
	        this.pushing = source["pushing"];
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

export namespace mods {
	
	export class Check {
	    name: string;
	    ok: boolean;
	    warn?: boolean;
	    detail?: string;
	
	    static createFrom(source: any = {}) {
	        return new Check(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.ok = source["ok"];
	        this.warn = source["warn"];
	        this.detail = source["detail"];
	    }
	}
	export class AuditEntry {
	    // Go type: time
	    at: any;
	    folder: string;
	    label: string;
	    phase: string;
	    gen: number;
	    ok: boolean;
	    summary: string;
	    checks: Check[];
	
	    static createFrom(source: any = {}) {
	        return new AuditEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.at = this.convertValues(source["at"], null);
	        this.folder = source["folder"];
	        this.label = source["label"];
	        this.phase = source["phase"];
	        this.gen = source["gen"];
	        this.ok = source["ok"];
	        this.summary = source["summary"];
	        this.checks = this.convertValues(source["checks"], Check);
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
	    held?: string[];
	    target: string;
	    notPruned?: string;
	
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
	        this.held = source["held"];
	        this.target = source["target"];
	        this.notPruned = source["notPruned"];
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
	export class ModFolder {
	    kind: string;
	    manager: string;
	    game: string;
	    gameName: string;
	    root: string;
	    rel: string;
	    role?: string;
	    sizeGB?: number;
	
	    static createFrom(source: any = {}) {
	        return new ModFolder(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.manager = source["manager"];
	        this.game = source["game"];
	        this.gameName = source["gameName"];
	        this.root = source["root"];
	        this.rel = source["rel"];
	        this.role = source["role"];
	        this.sizeGB = source["sizeGB"];
	    }
	}
	export class Settings {
	    theme: string;
	    backupEnabled: boolean;
	    backupRoot: string;
	    driveRoot: string;
	    backupBackend?: string;
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
	    noAutoUpdate: boolean;
	    noCloudPull: boolean;
	    noHoldWhilePlaying: boolean;
	    exclude?: Record<string, Array<string>>;
	    findMods: boolean;
	    autoAddMods: boolean;
	    syncDeployedMods: boolean;
	    shareVortexMods: boolean;
	    modsMaxGB: number;
	    // Go type: time
	    modsChanged: any;
	    mods?: Record<string, ModFolder>;
	    accounts?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.theme = source["theme"];
	        this.backupEnabled = source["backupEnabled"];
	        this.backupRoot = source["backupRoot"];
	        this.driveRoot = source["driveRoot"];
	        this.backupBackend = source["backupBackend"];
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
	        this.noAutoUpdate = source["noAutoUpdate"];
	        this.noCloudPull = source["noCloudPull"];
	        this.noHoldWhilePlaying = source["noHoldWhilePlaying"];
	        this.exclude = source["exclude"];
	        this.findMods = source["findMods"];
	        this.autoAddMods = source["autoAddMods"];
	        this.syncDeployedMods = source["syncDeployedMods"];
	        this.shareVortexMods = source["shareVortexMods"];
	        this.modsMaxGB = source["modsMaxGB"];
	        this.modsChanged = this.convertValues(source["modsChanged"], null);
	        this.mods = this.convertValues(source["mods"], ModFolder, true);
	        this.accounts = source["accounts"];
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

