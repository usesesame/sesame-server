import type { Plugin } from 'vite'

export declare const installPathOf: (id: string, root?: string) => string | null
export declare const shippedPackages: (build: string) => Plugin
