// Home, the self-service page's first tab: the setup card while this
// browser is not set up, three cards that say the state of what a person
// does here, and the waiting and lately decided requests under them.
import { useSelf } from "./context";
import { CredentialsCard, DevicesCard, RequestsCard } from "./home-cards";
import { RequestsTab } from "./requests-tab";
import { navigate } from "./router";
import { useDevices } from "./self-devices";
import { SetupCard } from "./setup-card";

// toWaiting moves focus to the "Waiting" heading the queue draws
// under the cards, where the person picks one of several requests.
function toWaiting() {
  const heading = document.querySelector<HTMLElement>("[data-home-waiting] h2");
  if (!heading) return;
  heading.setAttribute("tabindex", "-1");
  heading.scrollIntoView({ block: "start" });
  heading.focus();
}

export function HomeTab() {
  const { session, enrollment, waiting } = useSelf();
  const signedIn = session.kind === "signed-in";
  const devices = useDevices(signedIn);
  // Review opens the one request that waits on the Requests tab, and leads
  // to the list when several wait.
  const review = () => {
    if (Array.isArray(waiting) && waiting.length === 1) navigate("requests", true, waiting[0].id);
    else toWaiting();
  };
  return (
    <div className="flex flex-col gap-6" data-home>
      <SetupCard />
      <div className="grid gap-4 sm:grid-cols-3" data-cards>
        <RequestsCard onReview={review} devices={devices} />
        <CredentialsCard />
        <DevicesCard devices={devices} />
      </div>
      {/* Neither set up nor signed in, nothing can be read, and the first card already says why. */}
      {(enrollment || signedIn) && <RequestsTab home devices={devices} />}
    </div>
  );
}
