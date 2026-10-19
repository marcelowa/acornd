import { Type } from "@earendil-works/pi-ai";
import { defineExtension, defineTool } from "@earendil-works/pi-durable";
import { emit, log, type SecretInfo } from "./protocol.ts";

/**
 * The `secret` tool: what a workspace may use, and the one way a value can reach the vault
 * from a conversation. The value never comes back here — the card posts it to the gateway,
 * which writes it, and this side only learns the outcome. See
 * docs/adr/0021-a-vault-for-tool-secrets.md.
 */
interface PendingAnswer {
	resolve: (answer: SecretAnswer) => void;
}

interface PendingList {
	resolve: (answer: SecretListAnswer) => void;
}

export interface SecretAnswer {
	cancelled: boolean;
	scope?: "global" | "workspace" | undefined;
	restartRequired?: boolean | undefined;
	reason?: string | undefined;
}

export interface SecretListAnswer {
	secrets: SecretInfo[];
	reason?: string | undefined;
}

const answers = new Map<string, PendingAnswer>();
const lists = new Map<string, PendingList>();
let counter = 0;

function nextRequestId(): string {
	counter += 1;
	return `s${counter}`;
}

/** Control's answer to a request. Unknown ids are late answers to a dead tool; drop them. */
export function resolveSecretAnswer(requestId: string, answer: SecretAnswer): void {
	const pending = answers.get(requestId);
	if (pending === undefined) return;
	answers.delete(requestId);
	pending.resolve(answer);
}

export function resolveSecretList(requestId: string, answer: SecretListAnswer): void {
	const pending = lists.get(requestId);
	if (pending === undefined) return;
	lists.delete(requestId);
	pending.resolve(answer);
}

function askForSecret(
	conversationId: string,
	name: string,
	description: string,
	hosts: readonly string[],
): Promise<SecretAnswer> {
	const requestId = nextRequestId();
	return new Promise<SecretAnswer>((resolve) => {
		answers.set(requestId, { resolve });
		emit({
			type: "secret_request",
			id: requestId,
			conversationId,
			name,
			description,
			hosts: [...hosts],
		});
	});
}

function askForList(conversationId: string): Promise<SecretListAnswer> {
	const requestId = nextRequestId();
	return new Promise<SecretListAnswer>((resolve) => {
		lists.set(requestId, { resolve });
		emit({ type: "secret_list", id: requestId, conversationId });
	});
}

function formatList(answer: SecretListAnswer): string {
	if (answer.reason !== undefined && answer.reason !== "") {
		return `Could not read the secrets: ${answer.reason}`;
	}
	const secrets = answer.secrets ?? [];
	if (secrets.length === 0) {
		return "No secrets are available to this workspace.";
	}
	const lines = [
		"Secrets available to commands here (the values are never shown):",
		...secrets.map((secret) => {
			const description = secret.description ? `: ${secret.description}` : "";
			const status = secret.status ? ` — ${secret.status}` : "";
			const delivery = secret.delivery ? ` (${secret.delivery})` : "";
			const hosts = secret.hosts?.length ? ` for ${secret.hosts.join(", ")} over HTTPS` : "";
			const placeholder =
				secret.delivery === "substitute" && secret.placeholder
					? `; the env holds the literal ${secret.placeholder}`
					: "";
			return `- ${secret.name}${description}${status}${delivery}${hosts}${placeholder}`;
		}),
	];
	return lines.join("\n");
}

function answerText(answer: SecretAnswer): string {
	if (answer.cancelled) {
		const reason = answer.reason ? `: ${answer.reason}` : "";
		return `Not set${reason}.`;
	}
	const where = answer.scope === "global" ? "globally" : "to this workspace";
	const restart = answer.restartRequired
		? " Stop here; the sandbox will restart and I will continue automatically."
		: " The running sandbox has it.";
	return `Saved ${where}.${restart}`;
}

export function secretExtension() {
	return defineExtension({
		name: "acornfall-secrets",
		tools: [
			defineTool({
				name: "secret",
				description:
					"List the secrets this workspace may use, or ask the user to set one. A value is never in this conversation: use request and the user enters it in a card. Never ask the user to paste a secret as text.",
				parameters: Type.Object({
					action: Type.Union([Type.Literal("list"), Type.Literal("request")]),
					name: Type.Optional(
						Type.String({
							description:
								"Environment variable name for request, uppercase letters, digits, and underscores",
						}),
					),
					description: Type.Optional(
						Type.String({ description: "What the secret is for, shown in the list" }),
					),
					hosts: Type.Optional(
						Type.Array(Type.String(), {
							description:
								"Hosts the value may be substituted for, as a suggestion for the card",
						}),
					),
				}),
				execute: async (args, api) => {
					const conversationId = String(api.conversationId);
					if (args.action === "list") {
						const answer = await askForList(conversationId);
						return {
							output: [{ type: "text" as const, text: formatList(answer) }],
						};
					}
					if (args.name === undefined || !/^[A-Z][A-Z0-9_]*$/.test(args.name)) {
						log(`secret request refused: invalid name ${String(args.name)}`);
						return {
							output: [
								{
									type: "text" as const,
									text: "A secret needs a name that is an uppercase environment variable: A-Z, 0-9, and _.",
								},
							],
						};
					}
					const answer = await askForSecret(
						conversationId,
						args.name,
						args.description ?? "",
						args.hosts ?? [],
					);
					return {
						output: [{ type: "text" as const, text: answerText(answer) }],
					};
				},
			}),
		],
	});
}
