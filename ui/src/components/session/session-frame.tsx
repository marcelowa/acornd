import type { ReactNode } from "react";
import { BrandMark } from "@/components/layout/brand-mark";
import { Page } from "@/components/layout/page";
import { Stack } from "@/components/layout/stack";
import { Card, CardContent } from "@/components/ui/card";
import { Text } from "@/components/typography/text";

export interface SessionFrameProps {
	title: string;
	/** One sentence on what the screen is for. */
	intro: ReactNode;
	/** The form, or the one line a waiting screen says. */
	children?: ReactNode | undefined;
}

/**
 * The only screen a browser can reach with no cookie: the mark, a sentence, and one form. It is
 * its own frame rather than the console's `Shell`, because there is no rail, no transcript, and
 * nothing behind it to go back to.
 *
 * The width is the dialog step rather than a `Measure`: one question at a time is read in one
 * column, and a page of settings is a different column.
 */
export function SessionFrame({ title, intro, children }: SessionFrameProps) {
	return (
		<Page>
			<Stack fill justify="center" align="center" padding="lg" scroll="vertical">
				<Card className="w-full max-w-md">
					<CardContent>
						<Stack gap="md">
							<Stack gap="2xs">
								<BrandMark />
								<Text as="h1" size="xl" weight="semibold">
									{title}
								</Text>
								<Text size="sm" tone="muted">
									{intro}
								</Text>
							</Stack>
							{children}
						</Stack>
					</CardContent>
				</Card>
			</Stack>
		</Page>
	);
}

/**
 * The gateway's refusal, where the field was: it writes sentences a person can act on, and a form
 * that swallowed one would send them to guess (ADR 0036 §3).
 */
export function SessionRefusal({ children }: { children: ReactNode }) {
	return (
		<Text size="sm" className="text-destructive">
			{children}
		</Text>
	);
}

/** Where a secret comes from, in one line: the commands that print it, in the interface's code face. */
export function SessionSource({ children }: { children: ReactNode }) {
	return (
		<Text size="2xs" tone="muted">
			{children}
		</Text>
	);
}
