/**
 * The secrets control says a workspace can use, as the model is told about them: names,
 * descriptions, and how each one reaches the sandbox. The values are never here — a
 * substitute engine keeps them host-side, and an environment engine puts them in the guest's
 * process environment. See docs/adr/0021-a-vault-for-tool-secrets.md.
 */
export interface SecretInfo {
	name: string;
	description: string;
	hosts: readonly string[];
	delivery: "substitute" | "environment";
	/** The value of the guest's NAME variable on a substitute engine: a literal placeholder. */
	placeholder: string;
}

/** `--secrets` is a JSON array from control. A malformed answer means no section, not a crash. */
export function parseSecrets(raw: string | undefined): SecretInfo[] {
	if (raw === undefined || raw.trim() === "") return [];
	try {
		const parsed: unknown = JSON.parse(raw);
		if (!Array.isArray(parsed)) return [];
		const secrets: SecretInfo[] = [];
		for (const item of parsed) {
			if (typeof item !== "object" || item === null) continue;
			const record = item as Record<string, unknown>;
			const name = record["name"];
			if (typeof name !== "string" || name === "") continue;
			const hosts = Array.isArray(record["hosts"])
				? record["hosts"].filter((host): host is string => typeof host === "string")
				: [];
			const delivery = record["delivery"] === "environment" ? "environment" : "substitute";
			secrets.push({
				name,
				description: typeof record["description"] === "string" ? record["description"] : "",
				hosts,
				delivery,
				placeholder:
					typeof record["placeholder"] === "string"
						? record["placeholder"]
						: delivery === "substitute"
							? `$MSB_${name}`
							: "",
			});
		}
		return secrets;
	} catch {
		return [];
	}
}

/**
 * One prompt section. It says what exists and what a name is not; the tool that lists and
 * requests secrets arrives with the conversation surface. An empty set adds no section, so a
 * workspace with no secrets reads as if the feature were not there.
 */
export function secretSection(secrets: readonly SecretInfo[]): string | undefined {
	if (secrets.length === 0) return undefined;
	const lines = [
		"Secrets are available to commands as environment variables. A name is not its value: never print a secret, and never ask the user to paste one into the conversation. Use the secret tool to list them or ask the user to set one; the user enters the value in a card. For a substitute secret, the variable named NAME holds the literal placeholder $MSB_NAME: use $NAME in a shell, or send the literal $MSB_NAME directly; $MSB_NAME is not itself a variable. Send it over HTTPS: a plain HTTP request is not substituted everywhere. ${NAME} and {{NAME}} are not placeholders and pass through. A minimal image may not have curl; install one or use the project's HTTPS client.",
	];
	for (const secret of secrets) {
		const description = secret.description.trim() === "" ? "" : `${secret.description.trim()} `;
		const how =
			secret.delivery === "environment"
				? "The sandbox environment holds the real value."
				: secret.hosts.length === 0
					? "It is not delivered to the sandbox."
					: `${secret.name} is set to the literal ${secret.placeholder}; use $${secret.name} in a shell or send ${secret.placeholder} directly for ${secret.hosts.join(", ")} over HTTPS.`;
		lines.push(`- ${secret.name}: ${description}${how}`);
	}
	return lines.join("\n");
}
