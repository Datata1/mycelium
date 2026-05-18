export class CsEnv {
    private constructor(private readonly val: string | undefined) {}

    static empty(): CsEnv {
        return new CsEnv(undefined);
    }

    static from(val: string): CsEnv {
        return new CsEnv(val);
    }

    get isDev(): boolean {
        return this.val === "dev";
    }
}

export function getCsEnv(): string {
    return "getCsEnv";
}

export function setCsEnv(env: CsEnv): void {
    // no-op
}
