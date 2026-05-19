// types.d.ts — declaration-file fixture for v4 P0 dts-indexing bug.
//
// Mirrors the monorepo-4 shape that surfaced as F1/T1: a TypeScript
// type alias defined in a .d.ts file (`WorkspacePlan` in
// `Product.d.ts`) returning null on `find_symbol`. The fix needs to
// (a) walk this file and (b) emit symbols for the declarations. The
// integration test uses this fixture to pin both ends.

export interface WorkspacePlan {
    id: number;
    title: string;
    characteristics: {
        CPU: number;
        GPU: number;
        onDemand: boolean;
    };
    deprecated: boolean;
}

export type WorkspacePlanMap = Record<number, WorkspacePlan>;

export type PlanSelector = {cpu: number | 'smallest'};

export enum PlanTier {
    Free = 'Free',
    Micro = 'Micro',
    Boost = 'Boost',
    Pro = 'Pro',
}

declare module 'plan-helpers' {
    export function isFree(plan: WorkspacePlan): boolean;
}
