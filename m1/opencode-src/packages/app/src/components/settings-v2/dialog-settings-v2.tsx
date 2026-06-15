import { Component, createSignal, Show, For } from "solid-js"
import { PRIVACY, TERMS, EFFECTIVE, type LegalSection } from "./legal-content" // GRAMSAI_LEGAL
import { Dialog } from "@opencode-ai/ui/v2/dialog-v2"
import { TabsV2 } from "@opencode-ai/ui/v2/tabs-v2"
import { Icon } from "@opencode-ai/ui/icon"
import { useLanguage } from "@/context/language"
import { usePlatform } from "@/context/platform"
import { SettingsGeneralV2 } from "./general"
import { SettingsKeybinds } from "../settings-keybinds"
import { SettingsAccountV2 } from "./account"
import { SettingsBillingV2 } from "./billing"
import { SettingsDeveloperV2 } from "./developer"
import { SettingsSecurityV2 } from "./security"
import "./settings-v2.css"

export const DialogSettings: Component<{ initialTab?: string }> = (props) => {
  const language = useLanguage()
  const platform = usePlatform()
  const [legal, setLegal] = createSignal<null | "privacy" | "terms">(null) // GRAMSAI_LEGAL

  return (
    <Dialog size="x-large" class="settings-v2-dialog" data-component="settings-v2-dialog">
      <TabsV2
        orientation="vertical"
        variant="settings"
        defaultValue={props.initialTab ?? "account"}
        class="settings-v2"
        data-component="settings-v2"
      >
        <TabsV2.List>
          <div class="flex flex-col justify-between h-full w-full">
            <div class="flex flex-col gap-3 w-full">
              <div class="flex flex-col gap-3">
                <div class="flex flex-col gap-1.5">
                  <TabsV2.SectionTitle>Account</TabsV2.SectionTitle>
                  <div class="flex flex-col gap-1.5 w-full">
                    <TabsV2.Trigger value="account">
                      <Icon name="settings-gear" />
                      Account
                    </TabsV2.Trigger>
                    <TabsV2.Trigger value="billing">
                      <Icon name="arrow-down-to-line" />
                      Billing
                    </TabsV2.Trigger>
                    <TabsV2.Trigger value="developer">
                      <Icon name="code-lines" />
                      Developer
                    </TabsV2.Trigger>
                    <TabsV2.Trigger value="security">
                      <Icon name="providers" />
                      Privacy & Security
                    </TabsV2.Trigger>
                  </div>
                </div>

                <div class="flex flex-col gap-1.5">
                  <TabsV2.SectionTitle>{language.t("settings.section.desktop")}</TabsV2.SectionTitle>
                  <div class="flex flex-col gap-1.5 w-full">
                    <TabsV2.Trigger value="general">
                      <Icon name="sliders" />
                      {language.t("settings.tab.general")}
                    </TabsV2.Trigger>
                    <TabsV2.Trigger value="shortcuts">
                      <Icon name="keyboard" />
                      {language.t("settings.tab.shortcuts")}
                    </TabsV2.Trigger>
                  </div>
                </div>

              </div>
            </div>
            {/* GRAMSAI_LEGAL */}
            <div class="flex flex-col gap-1.5 w-full px-1 pb-2">
              <TabsV2.SectionTitle>Legal</TabsV2.SectionTitle>
              <button type="button" onClick={() => setLegal("terms")} style={{ display: "flex", "align-items": "center", gap: "8px", background: "transparent", border: "0", color: "var(--v2-text-text-muted, #8b939c)", "font-size": "13px", padding: "6px 8px", "border-radius": "8px", cursor: "pointer", "text-align": "left", width: "100%" }}>
                <Icon name="warning" />
                Terms &amp; Conditions
              </button>
              <button type="button" onClick={() => setLegal("privacy")} style={{ display: "flex", "align-items": "center", gap: "8px", background: "transparent", border: "0", color: "var(--v2-text-text-muted, #8b939c)", "font-size": "13px", padding: "6px 8px", "border-radius": "8px", cursor: "pointer", "text-align": "left", width: "100%" }}>
                <Icon name="providers" />
                Privacy Policy
              </button>
            </div>
            <div class="settings-v2-nav-footer">
              <span>{language.t("app.name.desktop")}</span>
              <span>v{platform.version}</span>
            </div>
          </div>
        </TabsV2.List>
        <TabsV2.Content value="general" class="settings-v2-panel">
          <SettingsGeneralV2 />
        </TabsV2.Content>
        <TabsV2.Content value="shortcuts" class="settings-v2-panel">
          <SettingsKeybinds v2 />
        </TabsV2.Content>
        <TabsV2.Content value="developer" class="settings-v2-panel">
          <SettingsDeveloperV2 />
        </TabsV2.Content>
        <TabsV2.Content value="security" class="settings-v2-panel">
          <SettingsSecurityV2 />
        </TabsV2.Content>
        <TabsV2.Content value="billing" class="settings-v2-panel">
          <SettingsBillingV2 />
        </TabsV2.Content>
        <TabsV2.Content value="account" class="settings-v2-panel">
          <SettingsAccountV2 />
        </TabsV2.Content>
      </TabsV2>
      <Show when={legal()}>
        <div onClick={() => setLegal(null)} style={{ position: "fixed", inset: "0", "z-index": "300", background: "rgba(0,0,0,.6)", display: "flex", "align-items": "center", "justify-content": "center", padding: "24px" }}>
          <div onClick={(e) => e.stopPropagation()} style={{ background: "#16181d", border: "1px solid #2a2f37", "border-radius": "14px", "max-width": "720px", width: "100%", "max-height": "82vh", display: "flex", "flex-direction": "column", "box-shadow": "0 24px 60px -16px rgba(0,0,0,.7)" }}>
            <div style={{ display: "flex", "align-items": "center", "justify-content": "space-between", padding: "16px 20px", "border-bottom": "1px solid #2a2f37" }}>
              <div style={{ "font-weight": "600", "font-size": "16px", color: "#e8eaed" }}>{legal() === "privacy" ? "Privacy Policy" : "Terms & Conditions"}</div>
              <button type="button" onClick={() => setLegal(null)} aria-label="Close" style={{ background: "transparent", border: "0", color: "#8b939c", "font-size": "13px", cursor: "pointer", padding: "4px 8px" }}>Close</button>
            </div>
            <div style={{ overflow: "auto", padding: "18px 22px" }}>
              <div style={{ "font-size": "12px", "font-style": "italic", color: "#8b939c", "margin-bottom": "16px" }}>{EFFECTIVE}</div>
              <For each={legal() === "privacy" ? PRIVACY : TERMS}>
                {(sec: LegalSection) => (
                  <div style={{ "margin-bottom": "18px" }}>
                    <div style={{ "font-weight": "600", "font-size": "14px", color: "#e8eaed", "margin-bottom": "7px" }}>{sec.h}</div>
                    <For each={sec.body}>{(para) => <p style={{ "font-size": "13px", "line-height": "1.6", color: "#c7ccd1", margin: "0 0 10px" }}>{para}</p>}</For>
                  </div>
                )}
              </For>
            </div>
          </div>
        </div>
      </Show>
    </Dialog>
  )
}
