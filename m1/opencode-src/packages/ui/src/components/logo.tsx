import { type ComponentProps } from "solid-js"

// grams hex + "g" mark. Replaces the original pixel logo so the loading splash
// and all logo placements are on-brand.
const HEX = "M12 2 3 7v10l9 5 9-5V7l-9-5Z"

export const Mark = (props: { class?: string }) => {
  return (
    <svg
      data-component="logo-mark"
      classList={{ [props.class ?? ""]: !!props.class }}
      viewBox="0 0 24 24"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
    >
      <path d={HEX} stroke="#3fb950" stroke-width="1.6" stroke-linejoin="round" />
      <text x="12" y="12.5" text-anchor="middle" dominant-baseline="central" font-size="11" font-weight="800" fill="#3fb950">g</text>
    </svg>
  )
}

export const Splash = (props: Pick<ComponentProps<"svg">, "ref" | "class">) => {
  return (
    <svg
      ref={props.ref}
      data-component="logo-splash"
      classList={{ [props.class ?? ""]: !!props.class }}
      viewBox="0 0 24 24"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
    >
      <path d={HEX} stroke="#3fb950" stroke-width="1.4" stroke-linejoin="round" />
      <text x="12" y="12.5" text-anchor="middle" dominant-baseline="central" font-size="11" font-weight="800" fill="#3fb950">g</text>
    </svg>
  )
}

export const Logo = (props: { class?: string }) => {
  // wordmark: green-g hex + "grams"
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 150 40"
      fill="none"
      classList={{ [props.class ?? ""]: !!props.class }}
    >
      <path d="M20 6 8 12v16l12 6 12-6V12L20 6Z" stroke="#3fb950" stroke-width="2.4" stroke-linejoin="round" />
      <text x="20" y="21" text-anchor="middle" dominant-baseline="central" font-size="15" font-weight="800" fill="#3fb950">g</text>
      <text x="44" y="21" dominant-baseline="central" font-family="-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif" font-size="22" font-weight="800" fill="var(--icon-strong-base)">grams</text>
    </svg>
  )
}
