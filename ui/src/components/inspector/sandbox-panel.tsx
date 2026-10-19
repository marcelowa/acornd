import type { SandboxSummary } from "@/app/view-models";
import { Fact } from "./fact";

export interface SandboxPanelProps {
	sandbox?: SandboxSummary | undefined;
	/** Where the active workspace keeps its bytes, when the engine reports it. */
	workspacePath?: string | undefined;
}

/**
 * Where the tools actually run. `isolated: false` is the host engine, and the interface has
 * to be able to say so: a fallback nobody can see is a fallback nobody agreed to.
 */
export function SandboxPanel({ sandbox, workspacePath }: SandboxPanelProps) {
	if (sandbox === undefined) {
		return (
			<div className="text-2xs text-muted-foreground">
				No sandbox state yet; the workspace has not been asked for one.
			</div>
		);
	}

	return (
		<div className="flex flex-col gap-0.5">
			<Fact
				label="engine"
				value={`${sandbox.engine}${sandbox.version === "" ? "" : ` ${sandbox.version}`}`}
			/>
			<Fact label="status" value={sandbox.status} />
			<Fact
				label="tools"
				value={sandbox.tools ? "live" : "refused"}
				tone={sandbox.tools ? undefined : "danger"}
			/>
			<Fact
				label="isolation"
				value={sandbox.isolated ? "isolated" : "the host: not isolated"}
				tone={sandbox.isolated ? undefined : "danger"}
			/>
			{sandbox.problem === undefined ? null : (
				<Fact label="problem" value={sandbox.problem} tone="danger" />
			)}
			{sandbox.secretsProblem === undefined ? null : (
				<Fact label="secrets" value={sandbox.secretsProblem} tone="danger" />
			)}
			{sandbox.secrets.length === 0 ? null : (
				<>
					<Fact
						label="secrets"
						value={
							sandbox.secretsRestart
								? "the next sandbox start applies a change"
								: `${sandbox.secrets.length}`
						}
					/>
					{sandbox.secrets.map((secret) => (
						<Fact
							key={secret.name}
							label={secret.name}
							value={[secret.status, secret.delivery].filter(Boolean).join(" · ")}
						/>
					))}
				</>
			)}
			{workspacePath === undefined ? null : <Fact label="workspace" value={workspacePath} />}
		</div>
	);
}
