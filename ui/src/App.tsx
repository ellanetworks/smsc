import { AppBar, Container, Stack, Toolbar, Typography } from "@mui/material";
import DiameterStatusCard from "@/components/DiameterStatusCard";
import MessagesSection from "@/components/MessagesSection";

export default function App() {
  return (
    <>
      <AppBar position="static" elevation={0}>
        <Toolbar>
          <Typography variant="h6" component="h1">
            Ella SMSC
          </Typography>
        </Toolbar>
      </AppBar>
      <Container component="main" maxWidth="lg" sx={{ py: 4 }}>
        <Stack spacing={3}>
          <DiameterStatusCard />
          <MessagesSection />
        </Stack>
      </Container>
    </>
  );
}
