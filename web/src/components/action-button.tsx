import type { HTMLMotionProps } from "motion/react";
import type { VariantProps } from "class-variance-authority";
import { Button as AnimateButton } from "./animate/button";
import { buttonVariants } from "./ui/button";
import { usePreferences } from "@/lib/preferences";
import { cn } from "@/lib/utils";

type Props = HTMLMotionProps<"button"> & VariantProps<typeof buttonVariants>;

export function ActionButton({ variant, size, className, type = "button", ...props }: Props) {
  const { reducedMotion } = usePreferences();
  return <AnimateButton {...props} asChild={false} type={type} hoverScale={reducedMotion ? 1 : 1.015} tapScale={reducedMotion ? 1 : 0.985} className={cn(buttonVariants({ variant, size }), "action-button", className)} />;
}
