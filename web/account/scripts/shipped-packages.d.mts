import type { Plugin } from 'vite'

export declare const buildConfigFiles: string[]
export declare const installPathOf: (id: string, root?: string) => string | null
export declare const inputPathOf: (id: string, root?: string, base?: string) => string | null
export declare const buildInputs: (ids: string[], root?: string, base?: string, configFile?: string | null) => { path: string; sha256: string }[]
export declare const shippedPackages: (build: string) => Plugin
