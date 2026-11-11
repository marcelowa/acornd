import { useEffect, useState } from "react";
import { downloadSupportBundle, fetchSupportPreview, type SupportPreview } from "@/app/client";
import { formatBytes } from "@/lib/format";
import { Button } from "@/components/ui/button";
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "@/components/ui/dialog";
import { Text } from "@/components/typography/text";

export interface SupportBundleDialogProps {
	workspace: string;
	open: boolean;
	onOpenChange: (open: boolean) => void;
}

/**
 * The owner's copy of the facts behind a sandbox failure: records, logs, and process groups,
 * with secrets removed. The preview comes first because a bundle is still a report about the
 * machine, and the download is the only thing that leaves this screen.
 */
export function SupportBundleDialog({ workspace, open, onOpenChange }: SupportBundleDialogProps) {
	const [preview, setPreview] = useState<SupportPreview | undefined>(undefined);
	const [failure, setFailure] = useState<string | undefined>(undefined);
	const [busy, setBusy] = useState(false);
	const [downloading, setDownloading] = useState(false);

	useEffect(() => {
		if (!open) return;
		let live = true;
		setPreview(undefined);
		setFailure(undefined);
		setBusy(true);
		fetchSupportPreview(workspace)
			.then((next) => {
				if (live) setPreview(next);
			})
			.catch((error: unknown) => {
				if (live) setFailure(error instanceof Error ? error.message : String(error));
			})
			.finally(() => {
				if (live) setBusy(false);
			});
		return () => {
			live = false;
		};
	}, [open, workspace]);

	const download = async (): Promise<void> => {
		setDownloading(true);
		setFailure(undefined);
		try {
			const { blob, filename } = await downloadSupportBundle(workspace);
			const url = URL.createObjectURL(blob);
			const anchor = document.createElement("a");
			anchor.href = url;
			anchor.download = filename;
			anchor.click();
			URL.revokeObjectURL(url);
		} catch (error) {
			setFailure(error instanceof Error ? error.message : String(error));
		} finally {
			setDownloading(false);
		}
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className="sm:max-w-lg">
				<DialogHeader>
					<DialogTitle>Support bundle</DialogTitle>
					<DialogDescription>
						A redacted copy of this workspace&apos;s records, logs, and process groups.
						It stays on this machine unless you share it.
					</DialogDescription>
				</DialogHeader>
				<div className="flex min-h-0 flex-col gap-3">
					{busy ? <Text size="sm">Collecting the diagnosis…</Text> : null}
					{failure === undefined ? null : (
						<Text size="sm" className="text-destructive">
							{failure}
						</Text>
					)}
					{preview === undefined ? null : (
						<>
							<div>
								<Text size="2xs" weight="semibold" tone="muted">
									WHAT WILL BE INCLUDED
								</Text>
								<div className="mt-1 max-h-48 overflow-y-auto rounded-md border border-border p-2 font-mono text-2xs">
									{preview.files.map((file) => (
										<div key={file.name} className="flex justify-between gap-4">
											<span className="truncate">{file.name}</span>
											<span className="shrink-0 text-muted-foreground">
												{formatBytes(file.size)}
											</span>
										</div>
									))}
								</div>
							</div>
							<div>
								<Text size="2xs" weight="semibold" tone="muted">
									WHAT WAS REMOVED
								</Text>
								<ul className="mt-1 space-y-0.5 text-2xs text-muted-foreground">
									{preview.redactions.map((note) => (
										<li key={note}>{note}</li>
									))}
								</ul>
							</div>
							{preview.warnings.length === 0 ? null : (
								<div>
									<Text size="2xs" weight="semibold" tone="muted">
										WARNINGS
									</Text>
									<ul className="mt-1 space-y-0.5 text-2xs text-muted-foreground">
										{preview.warnings.map((warning) => (
											<li key={warning}>{warning}</li>
										))}
									</ul>
								</div>
							)}
							<Text size="2xs" tone="muted">
								The file is not uploaded anywhere. Review it before you share it.
							</Text>
						</>
					)}
				</div>
				<DialogFooter>
					<Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
						Close
					</Button>
					<Button
						type="button"
						disabled={preview === undefined || downloading}
						onClick={() => {
							void download();
						}}
					>
						{downloading ? "Downloading…" : "Download bundle"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
