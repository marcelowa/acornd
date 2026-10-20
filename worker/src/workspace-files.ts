import { mkdir, readdir, readFile, rename, rm, stat, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { hostFiles, type DirEntry, type FileStat } from "./files.ts";
import type { WriteSink } from "./sandbox.ts";

/**
 * The file operations of an engine whose sandbox sees the host's paths unchanged: the host
 * engine, and bubblewrap, which binds a folder at the same path it already has.
 *
 * An engine that translates paths — a guest with its own root — implements these itself. This
 * exists so that the two that do not translate them cannot drift apart.
 */
export class WorkspaceFiles {
	read(path: string): Promise<string | null> {
		return hostFiles.read(path);
	}

	async list(path: string): Promise<DirEntry[]> {
		const entries = await readdir(path, { withFileTypes: true }).catch(() => []);
		return await Promise.all(
			entries.map(async (entry) => {
				const info = await stat(join(path, entry.name)).catch(() => null);
				return {
					name: entry.name,
					directory: entry.isDirectory(),
					size: info?.size ?? 0,
					mtimeMs: info?.mtimeMs ?? 0,
				};
			}),
		);
	}

	stat(path: string): Promise<FileStat | null> {
		return hostFiles.stat(path);
	}

	async readBytes(path: string): Promise<Uint8Array> {
		return new Uint8Array(await readFile(path));
	}

	async write(path: string, content: string | Uint8Array): Promise<void> {
		await writeFile(path, content);
	}

	async writeStream(path: string): Promise<WriteSink> {
		const writer = Bun.file(path).writer();
		return {
			write: async (data) => {
				await writer.write(data);
			},
			close: async () => {
				await writer.end();
			},
		};
	}

	async mkdir(path: string): Promise<void> {
		await mkdir(path, { recursive: true }).catch(() => {});
	}

	async remove(path: string, options: { recursive?: boolean; force?: boolean }): Promise<void> {
		await rm(path, {
			recursive: options.recursive === true,
			force: options.force === true,
		}).catch(() => {});
	}

	async rename(from: string, to: string): Promise<void> {
		await rename(from, to);
	}
}
