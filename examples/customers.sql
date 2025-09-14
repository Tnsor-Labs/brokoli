CREATE TABLE [customer] (
  [ID] INT,
  [FIRST_NAME] NVARCHAR(MAX),
  [LAST_NAME] NVARCHAR(MAX),
  [EMAIL] NVARCHAR(MAX),
  [COUNTRY] NVARCHAR(MAX),
  [CITY] NVARCHAR(MAX),
  [PHONE_NUMBER] NVARCHAR(MAX),
  [SUBSCRIPTION_DATE] DATE
);

INSERT INTO [customer] ([ID], [FIRST_NAME], [LAST_NAME], [EMAIL], [COUNTRY], [CITY], [PHONE_NUMBER], [SUBSCRIPTION_DATE])
SELECT * FROM (
  SELECT '1', 'John', 'Doe', 'john.doe@example.com', 'USA', 'New York', '+1-555-123-4567', '2023-01-15' UNION ALL
  SELECT '2', 'Jane', 'Smith', 'jane.smith@example.com', 'UK', 'London', '+44-20-1234-5678', '2023-02-20' UNION ALL
  SELECT '3', 'Michael', 'Johnson', 'michael.j@example.com', 'Canada', 'Toronto', '+1-416-555-7890', '2023-01-10' UNION ALL
  SELECT '4', 'Emily', 'Brown', 'emily.brown@example.com', 'Australia', 'Sydney', '+61-2-9876-5432', '2023-03-05' UNION ALL
  SELECT '5', 'David', 'Wilson', 'david.wilson@example.com', 'Germany', 'Berlin', '+49-30-1234-5678', '2023-02-28' UNION ALL
  SELECT '6', 'Sarah', 'Taylor', 'sarah.taylor@example.com', 'France', 'Paris', '+33-1-2345-6789', '2023-01-22' UNION ALL
  SELECT '7', 'Robert', 'Anderson', 'robert.a@example.com', 'USA', 'Chicago', '+1-312-555-6789', '2023-03-12' UNION ALL
  SELECT '8', 'Jennifer', 'Martinez', 'jennifer.m@example.com', 'Spain', 'Madrid', '+34-91-234-5678', '2023-02-05' UNION ALL
  SELECT '9', 'William', 'Thomas', 'william.t@example.com', 'Canada', 'Vancouver', '+1-604-555-1234', '2023-01-30' UNION ALL
  SELECT '10', 'Lisa', 'Garcia', 'lisa.garcia@example.com', 'Mexico', 'Mexico City', '+52-55-1234-5678', '2023-03-18'
) AS source;

