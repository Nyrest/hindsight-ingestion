import { Check, Copy } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";

export function CopyButton({ value, className }: { value: string; className?: string }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      toast.success(t("common.copied"));
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      toast.error(t("common.copyFailed"));
    }
  }
  return (
    <Button type="button" variant="outline" size="icon" className={className} onClick={copy} aria-label={t("common.copy")}>
      {copied ? <Check /> : <Copy />}
    </Button>
  );
}
